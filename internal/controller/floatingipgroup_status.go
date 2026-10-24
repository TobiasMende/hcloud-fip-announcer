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
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/TobiasMende/hcloud-fip-announcer/api/v1alpha1"
)

// finish writes the outcome into status, conditions and metrics and
// schedules the next poll.
func (r *FloatingIPGroupReconciler) finish(ctx context.Context, group *v1alpha1.FloatingIPGroup, out outcome) (ctrl.Result, error) {
	before := group.DeepCopy()
	st := &group.Status
	if out.fips != nil {
		st.IPv4, st.IPv6 = "", ""
		for _, f := range out.fips {
			if f.family == familyIPv4 {
				st.IPv4 = f.statusAddress()
			} else {
				st.IPv6 = f.statusAddress()
			}
		}
	}
	if m := out.record; m != nil && (out.moved || st.Node != m.to || st.Reason != m.reason) {
		since := metav1.NewTime(m.at)
		st.Node, st.Reason, st.Since = m.to, m.reason, &since
	}

	ready := metav1.Condition{Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse,
		Reason: out.reason, Message: out.message, ObservedGeneration: group.Generation}
	if out.ready {
		ready.Status = metav1.ConditionTrue
	}
	degraded := metav1.Condition{Type: v1alpha1.ConditionDegraded, Status: metav1.ConditionFalse,
		Reason: reasonAsExpected, ObservedGeneration: group.Generation}
	if out.degraded {
		degraded.Status, degraded.Reason, degraded.Message = metav1.ConditionTrue, out.reason, out.message
		if prev := meta.FindStatusCondition(before.Status.Conditions, v1alpha1.ConditionDegraded); prev == nil ||
			prev.Status != metav1.ConditionTrue || prev.Reason != out.reason {
			r.Recorder.Event(group, corev1.EventTypeWarning, out.reason, out.message)
		}
	}
	meta.SetStatusCondition(&st.Conditions, ready)
	meta.SetStatusCondition(&st.Conditions, degraded)
	if !out.keepPending {
		st.PendingMove = nil
	}

	r.updateMetrics(group, out)

	if !equality.Semantic.DeepEqual(before.Status, group.Status) {
		if err := r.Status().Patch(ctx, group, client.MergeFrom(before)); err != nil {
			return ctrl.Result{}, err
		}
		if out.announce && out.record != nil {
			r.announceMove(ctx, group, out.snap, out.fips, *out.record)
		}
	}

	requeue := r.PollInterval
	if out.requeue > 0 && out.requeue < requeue {
		requeue = out.requeue
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

func (r *FloatingIPGroupReconciler) updateMetrics(group *v1alpha1.FloatingIPGroup, out outcome) {
	name := group.Name
	initGroupMetrics(name)
	eligibleNodesGauge.WithLabelValues(name).Set(float64(out.eligible))
	degradedGauge.WithLabelValues(name).Set(boolFloat(out.degraded))
	if group.Status.Since != nil {
		lastMoveGauge.WithLabelValues(name).Set(float64(group.Status.Since.Unix()))
	}

	var series [][3]string
	if out.holder != "" {
		for _, ip := range fipAddresses(out.fips) {
			if ip != "" {
				series = append(series, [3]string{name, ip, out.holder})
			}
		}
	}
	r.mu.Lock()
	if r.assignments == nil {
		r.assignments = map[string][][3]string{}
	}
	old := r.assignments[name]
	r.assignments[name] = series
	r.mu.Unlock()
	for _, s := range old {
		if !slices.Contains(series, s) {
			assignmentGauge.DeleteLabelValues(s[:]...)
		}
	}
	for _, s := range series {
		assignmentGauge.WithLabelValues(s[:]...).Set(1)
	}
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
