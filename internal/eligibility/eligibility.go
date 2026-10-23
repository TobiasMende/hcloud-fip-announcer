// Package eligibility decides which nodes may hold a group's Floating IPs.
package eligibility

import (
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const controlPlaneLabel = "node-role.kubernetes.io/control-plane"

// Reason says why a node is ineligible. The empty Reason means eligible.
type Reason string

const (
	Cordoned          Reason = "Cordoned"
	NotReady          Reason = "NotReady"
	Deleting          Reason = "Deleting"
	SelectorMismatch  Reason = "SelectorMismatch"
	EndpointsNotReady Reason = "EndpointsNotReady"
	ProbeFailed       Reason = "ProbeFailed"
	NoServer          Reason = "NoServer"
	OtherZone         Reason = "OtherZone"
	ControlPlane      Reason = "ControlPlane"
)

// NodeKey identifies a probe result; Service is "namespace/name".
type NodeKey struct {
	Node    string
	Service string
}

// Input is the snapshot Eligible evaluates. ServerOf and ZoneOf must be non-nil.
// A selector that matches a control-plane node (including match-all) admits it.
type Input struct {
	Nodes    []corev1.Node
	Services []corev1.Service
	Slices   []discoveryv1.EndpointSlice
	// Selector is only consulted when SelectorSet is true.
	Selector    labels.Selector
	SelectorSet bool
	// Probes holds explicit results; a missing key means "no result yet" and does not fail.
	Probes   map[NodeKey]bool
	ServerOf func(corev1.Node) (int64, bool)
	Zone     string
	ZoneOf   func(serverID int64) string
}

// Eligible maps every input node to "" if eligible, else the reason.
func Eligible(in Input) map[string]Reason {
	ready := readyEndpoints(in.Slices)
	out := make(map[string]Reason, len(in.Nodes))
	for _, n := range in.Nodes {
		out[n.Name] = reasonFor(in, n, ready)
	}
	return out
}

func reasonFor(in Input, n corev1.Node, ready map[NodeKey]bool) Reason {
	switch {
	case n.Spec.Unschedulable:
		return Cordoned
	case n.DeletionTimestamp != nil:
		return Deleting
	case !nodeReady(n):
		return NotReady
	}
	selected := in.SelectorSet && in.Selector != nil && in.Selector.Matches(labels.Set(n.Labels))
	if in.SelectorSet && !selected {
		return SelectorMismatch
	}
	if _, cp := n.Labels[controlPlaneLabel]; cp && !selected {
		return ControlPlane
	}
	if len(in.Services) == 0 && !in.SelectorSet {
		return SelectorMismatch
	}
	for _, s := range in.Services {
		key := NodeKey{Node: n.Name, Service: s.Namespace + "/" + s.Name}
		if !ready[key] {
			return EndpointsNotReady
		}
		if ok, has := in.Probes[key]; has && !ok {
			return ProbeFailed
		}
	}
	id, ok := in.ServerOf(n)
	if !ok {
		return NoServer
	}
	if in.ZoneOf(id) != in.Zone {
		return OtherZone
	}
	return ""
}

func nodeReady(n corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func readyEndpoints(slices []discoveryv1.EndpointSlice) map[NodeKey]bool {
	ready := map[NodeKey]bool{}
	for _, s := range slices {
		svc := s.Labels[discoveryv1.LabelServiceName]
		if svc == "" {
			continue
		}
		for _, e := range s.Endpoints {
			if e.NodeName == nil || e.Conditions.Ready == nil || !*e.Conditions.Ready {
				continue
			}
			ready[NodeKey{Node: *e.NodeName, Service: s.Namespace + "/" + svc}] = true
		}
	}
	return ready
}
