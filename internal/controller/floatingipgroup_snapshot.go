/*
Copyright 2026 Tobias Mende.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/TobiasMende/hcloud-fip-announcer/api/v1alpha1"
	"github.com/TobiasMende/hcloud-fip-announcer/internal/eligibility"
	"github.com/TobiasMende/hcloud-fip-announcer/internal/hetzner"
	"github.com/TobiasMende/hcloud-fip-announcer/internal/probe"
)

const (
	providerIDPrefix = "hcloud://"
	serverStatusOff  = "off"
)

// groupFIP is one requested Floating IP of a group with its Hetzner state.
type groupFIP struct {
	family string
	ref    *v1alpha1.FloatingIPRef
	fip    *hetzner.FloatingIP
}

// statusAddress is the address written into status: the IPv4 itself, or the
// configured address inside the IPv6 /64.
func (g groupFIP) statusAddress() string {
	if g.fip.IP == nil {
		return ""
	}
	if g.family == familyIPv4 {
		return g.fip.IP.String()
	}
	network := g.fip.Network
	if network == nil {
		network = &net.IPNet{IP: g.fip.IP, Mask: net.CIDRMask(64, 128)}
	}
	host := net.ParseIP(g.ref.IPv6Address)
	if host == nil {
		host = net.ParseIP("::1")
	}
	base := network.IP.To16()
	out := make(net.IP, net.IPv6len)
	for i := range out {
		out[i] = base[i]&network.Mask[i] | host[i]&^network.Mask[i]
	}
	return out.String()
}

func requestedFIPs(group *v1alpha1.FloatingIPGroup) []groupFIP {
	var out []groupFIP
	if group.Spec.IPv4 != nil {
		out = append(out, groupFIP{family: familyIPv4, ref: group.Spec.IPv4})
	}
	if group.Spec.IPv6 != nil {
		out = append(out, groupFIP{family: familyIPv6, ref: group.Spec.IPv6})
	}
	return out
}

// snapshot is the cluster state one reconcile decides on.
type snapshot struct {
	nodes    []corev1.Node
	byName   map[string]*corev1.Node
	services []corev1.Service
	slices   []discoveryv1.EndpointSlice
	// serverToNode maps Hetzner server IDs to cluster nodes.
	serverToNode map[int64]string
}

func (r *FloatingIPGroupReconciler) loadSnapshot(ctx context.Context, group *v1alpha1.FloatingIPGroup) (*snapshot, error) {
	nodes := &corev1.NodeList{}
	if err := r.List(ctx, nodes); err != nil {
		return nil, err
	}
	svcs := &corev1.ServiceList{}
	if err := r.List(ctx, svcs, client.MatchingFields{ServiceGroupIndex: group.Name}); err != nil {
		return nil, err
	}
	s := &snapshot{nodes: nodes.Items, byName: map[string]*corev1.Node{}, serverToNode: map[int64]string{}}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		s.byName[n.Name] = n
		if id, ok := parseProviderID(n.Spec.ProviderID); ok {
			s.serverToNode[id] = n.Name
		}
	}
	for _, svc := range svcs.Items {
		if !handlesClass(&svc, r.Class) {
			continue
		}
		s.services = append(s.services, svc)
		list := &discoveryv1.EndpointSliceList{}
		if err := r.List(ctx, list, client.InNamespace(svc.Namespace),
			client.MatchingLabels{discoveryv1.LabelServiceName: svc.Name}); err != nil {
			return nil, err
		}
		s.slices = append(s.slices, list.Items...)
	}
	return s, nil
}

func parseProviderID(id string) (int64, bool) {
	rest, ok := strings.CutPrefix(id, providerIDPrefix)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	return n, err == nil && n > 0
}

// serverOf maps a node to its Hetzner server: providerID first, else a
// server with the node's name.
func (r *FloatingIPGroupReconciler) serverOf(ctx context.Context, n *corev1.Node) (int64, error) {
	if id, ok := parseProviderID(n.Spec.ProviderID); ok {
		return id, nil
	}
	s, err := r.hcache().serverByName(ctx, n.Name)
	if err != nil {
		return 0, err
	}
	return s.ID, nil
}

// nodeOfServer finds the cluster node of a Hetzner server, "" if none.
func (r *FloatingIPGroupReconciler) nodeOfServer(ctx context.Context, s *snapshot, id int64) (string, error) {
	if name, ok := s.serverToNode[id]; ok {
		return name, nil
	}
	for i := range s.nodes {
		n := &s.nodes[i]
		if _, ok := parseProviderID(n.Spec.ProviderID); ok {
			continue
		}
		sid, err := r.serverOf(ctx, n)
		if errors.Is(err, hetzner.ErrNotFound) {
			continue
		}
		if err != nil {
			return "", err
		}
		if sid == id {
			return n.Name, nil
		}
	}
	return "", nil
}

// eligible evaluates every node. Hetzner lookups go through the cache; an
// API error aborts the evaluation rather than counting as ineligible.
func (r *FloatingIPGroupReconciler) eligible(ctx context.Context, group *v1alpha1.FloatingIPGroup, s *snapshot,
	zone string) (map[string]eligibility.Reason, error) {
	var apiErr error
	in := eligibility.Input{
		Nodes:    s.nodes,
		Services: s.services,
		Slices:   s.slices,
		Probes:   r.Prober.Results(),
		Zone:     zone,
		ServerOf: func(n corev1.Node) (int64, bool) {
			id, err := r.serverOf(ctx, &n)
			if err != nil && !errors.Is(err, hetzner.ErrNotFound) {
				apiErr = errors.Join(apiErr, err)
			}
			return id, err == nil
		},
		ZoneOf: func(id int64) string {
			srv, err := r.hcache().server(ctx, id)
			if err != nil {
				if !errors.Is(err, hetzner.ErrNotFound) {
					apiErr = errors.Join(apiErr, err)
				}
				return ""
			}
			if srv.Status == serverStatusOff {
				return ""
			}
			return srv.NetworkZone
		},
	}
	if group.Spec.NodeSelector != nil {
		sel, err := metav1.LabelSelectorAsSelector(group.Spec.NodeSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid nodeSelector: %w", err)
		}
		in.Selector, in.SelectorSet = sel, true
	}
	reasons := eligibility.Eligible(in)
	return reasons, apiErr
}

func eligibleNodes(reasons map[string]eligibility.Reason) []string {
	var out []string
	for n, reason := range reasons {
		if reason == "" {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// probeTargets returns one target per (node, member Service) with at least
// one ready local endpoint and something to probe.
func (r *FloatingIPGroupReconciler) probeTargets(group string, s *snapshot) []probe.Target {
	byKey := map[eligibility.NodeKey]*probe.Target{}
	for _, svc := range s.services {
		key := svc.Namespace + "/" + svc.Name
		for _, slice := range s.slices {
			if slice.Namespace != svc.Namespace || slice.Labels[discoveryv1.LabelServiceName] != svc.Name {
				continue
			}
			check, ok, err := probe.CheckFor(svc, slice)
			r.reportHealthCheck(group, &svc, err)
			if !ok {
				continue
			}
			for _, ep := range slice.Endpoints {
				if ep.NodeName == nil || ep.Conditions.Ready == nil || !*ep.Conditions.Ready {
					continue
				}
				ips := parseIPs(ep.Addresses)
				if len(ips) == 0 {
					continue
				}
				nk := eligibility.NodeKey{Node: *ep.NodeName, Service: key}
				t, found := byKey[nk]
				if !found {
					t = &probe.Target{Node: *ep.NodeName, Service: key, Check: check}
					byKey[nk] = t
				}
				t.PodIPs = append(t.PodIPs, ips...)
			}
		}
	}
	r.pruneHealthErrs(group, s.services)
	out := make([]probe.Target, 0, len(byKey))
	for _, k := range slices.SortedFunc(maps.Keys(byKey), func(a, b eligibility.NodeKey) int {
		return strings.Compare(a.Node+"\x00"+a.Service, b.Node+"\x00"+b.Service)
	}) {
		out = append(out, *byKey[k])
	}
	return out
}

// pruneHealthErrs forgets health-check errors of Services that are no
// longer members of the group.
func (r *FloatingIPGroupReconciler) pruneHealthErrs(group string, members []corev1.Service) {
	keep := map[string]bool{}
	for _, svc := range members {
		keep[group+"\x00"+svc.Namespace+"/"+svc.Name] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.healthErrs {
		if strings.HasPrefix(k, group+"\x00") && !keep[k] {
			delete(r.healthErrs, k)
		}
	}
}

// reportHealthCheck emits InvalidHealthCheck only when the error of a
// Service's health check changes.
func (r *FloatingIPGroupReconciler) reportHealthCheck(group string, svc *corev1.Service, err error) {
	key := group + "\x00" + svc.Namespace + "/" + svc.Name
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	r.mu.Lock()
	prev := r.healthErrs[key]
	if msg == "" {
		delete(r.healthErrs, key)
	} else {
		if r.healthErrs == nil {
			r.healthErrs = map[string]string{}
		}
		r.healthErrs[key] = msg
	}
	r.mu.Unlock()
	if msg != "" && msg != prev {
		r.Recorder.Event(svc, corev1.EventTypeWarning, "InvalidHealthCheck", msg)
	}
}

func parseIPs(addrs []string) []net.IP {
	var out []net.IP
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil {
			out = append(out, ip)
		}
	}
	return out
}

// moveReason maps why the holder is ineligible to the status reason.
func moveReason(r eligibility.Reason) v1alpha1.MoveReason {
	switch r {
	case eligibility.Cordoned:
		return v1alpha1.MoveReasonNodeCordoned
	case eligibility.NoServer:
		return v1alpha1.MoveReasonServerGone
	case eligibility.ProbeFailed:
		return v1alpha1.MoveReasonProbeFailed
	case eligibility.EndpointsNotReady:
		return v1alpha1.MoveReasonEndpointsNotReady
	default:
		return v1alpha1.MoveReasonNodeNotReady
	}
}
