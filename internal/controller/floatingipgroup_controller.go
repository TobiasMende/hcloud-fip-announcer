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
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/TobiasMende/hcloud-fip-announcer/api/v1alpha1"
	"github.com/TobiasMende/hcloud-fip-announcer/internal/eligibility"
	"github.com/TobiasMende/hcloud-fip-announcer/internal/hetzner"
	"github.com/TobiasMende/hcloud-fip-announcer/internal/placement"
	"github.com/TobiasMende/hcloud-fip-announcer/internal/probe"
)

const (
	finalizerName  = "fip-announcer.inocea.de/finalizer"
	clusterIDLabel = "fip-announcer.inocea.de/cluster-id"
	groupLabel     = "fip-announcer.inocea.de/group"

	familyIPv4 = "ipv4"
	familyIPv6 = "ipv6"

	reasonRefused          = "Refused"
	reasonNoEligibleNode   = "NoEligibleNode"
	reasonHolderIneligible = "HolderIneligible"
	reasonAssigned         = "Assigned"
	reasonAsExpected       = "AsExpected"
	reasonMoved            = "Moved"
	reasonNoMembers        = "NoMembers"

	defaultReconcileTimeout = 30 * time.Second
)

// errHolderChanged means the Hetzner holder read right before a move differs
// from the cached one; the decision is made again on fresh data.
var errHolderChanged = errors.New("floating IP holder changed since the last poll")

// Prober is the part of probe.Prober the reconciler uses.
type Prober interface {
	SetTargets(group string, targets []probe.Target)
	Results() map[eligibility.NodeKey]bool
}

// FloatingIPGroupReconciler keeps the Floating IPs of a group on one
// eligible node and records where they are in the group status.
// It relies on the ServiceGroupIndex registered by ServiceReconciler.
// Different groups reconcile concurrently; all shared state below is locked.
type FloatingIPGroupReconciler struct {
	client.Client
	// APIReader reads the group uncached so a decision never starts from a
	// status older than the last write.
	APIReader   client.Reader
	Scheme      *runtime.Scheme
	Hetzner     hetzner.Client
	Recorder    record.EventRecorder
	Prober      Prober
	ProbeEvents <-chan event.GenericEvent
	ClusterID   string
	// Class is the loadBalancerClass of member Services; "" means none.
	Class        string
	AntiFlap     time.Duration
	PollInterval time.Duration
	// LeaderNode is the node this pod runs on; placement avoids it on ties.
	LeaderNode string
	Now        func() time.Time
	// MaxConcurrentReconciles defaults to 1 when 0.
	MaxConcurrentReconciles int
	// ReconcileTimeout bounds one reconcile; 0 means 30s.
	ReconcileTimeout time.Duration

	cacheOnce sync.Once
	cache     *hetznerCache

	mu sync.Mutex
	// pending holds moves whose status write has not succeeded yet.
	pending map[string]moveInfo
	// assignments holds the assignment metric series last set per group.
	assignments map[string][][3]string
	// healthErrs holds the last invalid health-check message per Service.
	healthErrs map[string]string
}

type moveInfo struct {
	from, to  string
	reason    v1alpha1.MoveReason
	at        time.Time
	announced bool
}

// +kubebuilder:rbac:groups=fip-announcer.inocea.de,resources=floatingipgroups,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=fip-announcer.inocea.de,resources=floatingipgroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=fip-announcer.inocea.de,resources=floatingipgroups/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=services/status,verbs=get;patch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *FloatingIPGroupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	timeout := r.ReconcileTimeout
	if timeout <= 0 {
		timeout = defaultReconcileTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	group := &v1alpha1.FloatingIPGroup{}
	if err := r.APIReader.Get(ctx, req.NamespacedName, group); err != nil {
		if apierrors.IsNotFound(err) {
			r.forget(req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !group.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, group)
	}
	if controllerutil.AddFinalizer(group, finalizerName) {
		if err := r.Update(ctx, group); err != nil {
			return ctrl.Result{}, err
		}
	}
	// A failed refresh keeps the last snapshot (moves still re-read their
	// FIPs first) and polls again after refreshBackoff, never faster.
	stale := r.hcache().refresh(ctx) != nil
	if stale && !r.hcache().ready() {
		return ctrl.Result{RequeueAfter: refreshBackoff}, nil
	}
	res, err := r.sync(ctx, group)
	if errors.Is(err, errHolderChanged) {
		res, err = r.sync(ctx, group)
	}
	if errors.Is(err, errHolderChanged) {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	if stale && err == nil && (res.RequeueAfter == 0 || res.RequeueAfter > refreshBackoff) {
		res.RequeueAfter = refreshBackoff
	}
	return res, err
}

// outcome is what one sync observed and decided; finish writes it.
type outcome struct {
	fips     []groupFIP // nil leaves the status addresses untouched
	eligible int
	holder   string // node every FIP is on, for the assignment metric
	// record sets status node, reason and since when they differ, or always
	// with moved; nil keeps them.
	record   *moveInfo
	moved    bool
	ready    bool
	reason   string
	message  string
	degraded bool
	requeue  time.Duration
}

func (r *FloatingIPGroupReconciler) sync(ctx context.Context, group *v1alpha1.FloatingIPGroup) (ctrl.Result, error) {
	fips, refusal, err := r.resolveFIPs(ctx, group)
	if err != nil {
		return ctrl.Result{}, err
	}
	if refusal != "" {
		r.Prober.SetTargets(group.Name, nil)
		return r.finish(ctx, group, outcome{reason: reasonRefused, message: refusal, degraded: true})
	}

	snap, err := r.loadSnapshot(ctx, group)
	if err != nil {
		return ctrl.Result{}, err
	}
	r.Prober.SetTargets(group.Name, r.probeTargets(group.Name, snap))

	h, err := r.findHolder(ctx, group, snap, fips)
	if err != nil {
		return ctrl.Result{}, err
	}
	if h.foreign != "" {
		ext := v1alpha1.MoveReasonExternal
		return r.finish(ctx, group, outcome{
			record: &moveInfo{reason: ext, at: r.now()}, reason: string(ext), message: h.foreign, degraded: true,
		})
	}
	if err := r.ensureLabels(ctx, group, fips); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensureProtection(ctx, fips); err != nil {
		return ctrl.Result{}, err
	}
	if len(snap.services) == 0 && group.Spec.NodeSelector == nil {
		return r.finish(ctx, group, outcome{fips: fips, holder: h.alignedNode(fips), reason: reasonNoMembers,
			message: "No member Services and no nodeSelector; the Floating IPs are not placed"})
	}

	reasons, err := r.eligible(ctx, group, snap, fips[0].fip.NetworkZone)
	if err != nil {
		return ctrl.Result{}, err
	}
	eligible := eligibleNodes(reasons)
	counts, err := r.fipCounts(ctx, group.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	in := placement.Input{
		Holder:     h.node,
		Eligible:   eligible,
		Now:        r.now(),
		AntiFlap:   r.AntiFlap,
		LeaderNode: r.LeaderNode,
		FIPCount:   counts,
	}
	if group.Status.Since != nil {
		in.LastMove = group.Status.Since.Time
	}
	holderReason, known := reasons[h.node]
	holderEligible := known && holderReason == ""
	if h.node != "" && !holderEligible {
		in.HolderReason = moveReason(holderReason)
		if n := snap.byName[h.node]; n != nil && n.Spec.Unschedulable {
			in.HolderCordoned = true
		} else if h.serverGone {
			in.HolderReason = v1alpha1.MoveReasonServerGone
		}
		in.HolderServerGone = h.serverGone || h.nodeGone
	}

	out := outcome{fips: fips, eligible: len(eligible)}
	switch d := placement.Decide(in); d.Action {
	case placement.Degraded:
		out.holder = h.alignedNode(fips)
		out.reason, out.degraded = reasonNoEligibleNode, true
		out.message = "No eligible node; the Floating IPs stay where they are"
	case placement.Stay:
		if !holderEligible {
			out.holder = h.alignedNode(fips)
			out.reason = reasonHolderIneligible
			out.message = fmt.Sprintf("Node %s is ineligible (%s); waiting for the anti-flap interval", h.node, in.HolderReason)
			out.requeue = r.AntiFlap - in.Now.Sub(in.LastMove)
			break
		}
		if err := r.assign(ctx, fips, h.server); err != nil {
			return ctrl.Result{}, err
		}
		out.holder, out.ready, out.reason = h.node, true, reasonAssigned
		out.message = fmt.Sprintf("Floating IPs are on eligible node %s", h.node)
		if group.Status.Node != h.node {
			out.record = r.observed(ctx, group, snap, fips, h.node)
		}
	case placement.Move:
		sid, err := r.serverOf(ctx, snap.byName[d.Target])
		if err != nil {
			return ctrl.Result{}, err
		}
		// Pending before the first assign: if the move is interrupted, the
		// retry that finds the FIPs on the target completes this move.
		m := moveInfo{from: h.node, to: d.Target, reason: d.Reason, at: in.Now}
		r.setPending(group.Name, m)
		if err := r.assign(ctx, fips, sid); err != nil {
			if errors.Is(err, errHolderChanged) {
				r.clearPending(group.Name)
			}
			return ctrl.Result{}, err
		}
		r.announceMove(ctx, group, snap, fips, m)
		m.announced = true
		r.setPending(group.Name, m)
		out.holder, out.record, out.moved, out.ready, out.reason = d.Target, &m, true, true, reasonAssigned
		out.message = fmt.Sprintf("Floating IPs are on eligible node %s", d.Target)
	}
	return r.finish(ctx, group, out)
}

// observed records a holder the controller did not just move the FIPs to:
// the completion of its own move whose status write failed, or an
// assignment made outside the controller.
func (r *FloatingIPGroupReconciler) observed(ctx context.Context, group *v1alpha1.FloatingIPGroup, snap *snapshot,
	fips []groupFIP, node string) *moveInfo {
	r.mu.Lock()
	m, ok := r.pending[group.Name]
	r.mu.Unlock()
	if ok && m.to == node {
		if !m.announced {
			r.announceMove(ctx, group, snap, fips, m)
			m.announced = true
			r.setPending(group.Name, m)
		}
		return &m
	}
	m = moveInfo{from: group.Status.Node, to: node, reason: v1alpha1.MoveReasonExternal, at: r.now()}
	movesTotal.WithLabelValues(group.Name, string(m.reason)).Inc()
	logf.FromContext(ctx).Info("Floating IPs found on another node",
		"group", group.Name, "ips", fipAddresses(fips), "from", m.from, "to", m.to, "reason", m.reason)
	r.Recorder.Eventf(group, corev1.EventTypeNormal, reasonMoved,
		"Floating IPs found on eligible node %s (was %q), assigned outside the controller", node, m.from)
	return &m
}

func fipAddresses(fips []groupFIP) []string {
	out := make([]string, 0, len(fips))
	for _, f := range fips {
		out = append(out, f.statusAddress())
	}
	return out
}

// announceMove counts, logs and reports a move the controller made.
func (r *FloatingIPGroupReconciler) announceMove(ctx context.Context, group *v1alpha1.FloatingIPGroup, snap *snapshot,
	fips []groupFIP, m moveInfo) {
	ips := fipAddresses(fips)
	movesTotal.WithLabelValues(group.Name, string(m.reason)).Inc()
	logf.FromContext(ctx).Info("Moved Floating IPs",
		"group", group.Name, "ips", ips, "from", m.from, "to", m.to, "reason", m.reason)
	msg := fmt.Sprintf("Moved Floating IPs %s of group %s from %q to %q (%s)",
		strings.Join(ips, ", "), group.Name, m.from, m.to, m.reason)
	r.Recorder.Event(group, corev1.EventTypeNormal, reasonMoved, msg)
	for _, name := range []string{m.from, m.to} {
		if n := snap.byName[name]; n != nil {
			r.Recorder.Event(n, corev1.EventTypeNormal, reasonMoved, msg)
		}
	}
	for i := range snap.services {
		r.Recorder.Event(&snap.services[i], corev1.EventTypeNormal, reasonMoved, msg)
	}
}

// fipCounts counts, per node, the other groups whose FIPs it holds.
func (r *FloatingIPGroupReconciler) fipCounts(ctx context.Context, self string) (map[string]int, error) {
	groups := &v1alpha1.FloatingIPGroupList{}
	if err := r.List(ctx, groups); err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, g := range groups.Items {
		if g.Name != self && g.Status.Node != "" {
			counts[g.Status.Node]++
		}
	}
	return counts, nil
}

func (r *FloatingIPGroupReconciler) finalize(ctx context.Context, group *v1alpha1.FloatingIPGroup) error {
	if !controllerutil.ContainsFinalizer(group, finalizerName) {
		return nil
	}
	if err := r.releaseMembers(ctx, group.Name); err != nil {
		return err
	}
	if group.Spec.DeletionPolicy == v1alpha1.DeletionPolicyDelete {
		for _, g := range requestedFIPs(group) {
			f, err := r.Hetzner.FloatingIPByName(ctx, g.ref.Name)
			if errors.Is(err, hetzner.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if f.Labels[clusterIDLabel] != r.ClusterID || f.Labels[groupLabel] != group.Name {
				r.Recorder.Eventf(group, corev1.EventTypeWarning, "NotDeleted",
					"Floating IP %s is not labelled for this group; leaving it", f.Name)
				continue
			}
			if err := r.Hetzner.Delete(ctx, f.ID); err != nil {
				return fmt.Errorf("delete floating IP %s: %w", f.Name, err)
			}
			r.Recorder.Eventf(group, corev1.EventTypeNormal, "Deleted", "Deleted Floating IP %s (%s)", f.Name, f.IP)
		}
	}
	r.forget(group.Name)
	controllerutil.RemoveFinalizer(group, finalizerName)
	return client.IgnoreNotFound(r.Update(ctx, group))
}

// releaseMembers removes the group's addresses from its member Services, so
// none of them advertises a Floating IP that is about to be released.
func (r *FloatingIPGroupReconciler) releaseMembers(ctx context.Context, group string) error {
	svcs := &corev1.ServiceList{}
	if err := r.List(ctx, svcs, client.MatchingFields{ServiceGroupIndex: group}); err != nil {
		return err
	}
	for i := range svcs.Items {
		if svc := &svcs.Items[i]; handlesClass(svc, r.Class) {
			if err := releaseService(ctx, r.Client, r.Class, svc); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *FloatingIPGroupReconciler) forget(group string) {
	r.Prober.SetTargets(group, nil)
	forgetGroupMetrics(group)
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pending, group)
	delete(r.assignments, group)
	for k := range r.healthErrs {
		if strings.HasPrefix(k, group+"\x00") {
			delete(r.healthErrs, k)
		}
	}
}

func (r *FloatingIPGroupReconciler) clearPending(group string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pending, group)
}

func (r *FloatingIPGroupReconciler) setPending(group string, m moveInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending == nil {
		r.pending = map[string]moveInfo{}
	}
	r.pending[group] = m
}

func (r *FloatingIPGroupReconciler) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

// hcache returns the Hetzner snapshot, created on first use.
func (r *FloatingIPGroupReconciler) hcache() *hetznerCache {
	r.cacheOnce.Do(func() {
		r.cache = newHetznerCache(r.Hetzner, r.PollInterval, r.now)
	})
	return r.cache
}
