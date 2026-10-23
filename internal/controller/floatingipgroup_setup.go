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
	"maps"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/TobiasMende/hcloud-fip-announcer/api/v1alpha1"
)

// SetupWithManager watches groups, Nodes, member Services, their
// EndpointSlices and the prober's result changes. Every reconcile also
// requeues after the poll interval, so a dropped probe event only delays.
func (r *FloatingIPGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.FloatingIPGroup{}).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.allGroups),
			builder.WithPredicates(predicate.Funcs{UpdateFunc: nodeChanged})).
		Watches(&corev1.Service{}, handler.EnqueueRequestsFromMapFunc(groupOfService)).
		Watches(&discoveryv1.EndpointSlice{}, handler.EnqueueRequestsFromMapFunc(r.groupOfSlice)).
		Named("floatingipgroup").
		WithOptions(controller.Options{MaxConcurrentReconciles: r.MaxConcurrentReconciles})
	if r.ProbeEvents != nil {
		b = b.WatchesRawSource(source.Channel(r.ProbeEvents, &handler.EnqueueRequestForObject{}))
	}
	return b.Complete(r)
}

func (r *FloatingIPGroupReconciler) allGroups(ctx context.Context, _ client.Object) []reconcile.Request {
	groups := &v1alpha1.FloatingIPGroupList{}
	if err := r.List(ctx, groups); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(groups.Items))
	for _, g := range groups.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: g.Name}})
	}
	return reqs
}

func groupOfService(_ context.Context, obj client.Object) []reconcile.Request {
	if g := obj.GetAnnotations()[serviceGroupAnnotation]; g != "" {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: g}}}
	}
	return nil
}

func (r *FloatingIPGroupReconciler) groupOfSlice(ctx context.Context, obj client.Object) []reconcile.Request {
	name := obj.GetLabels()[discoveryv1.LabelServiceName]
	if name == "" {
		return nil
	}
	svc := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: obj.GetNamespace(), Name: name}, svc); err != nil {
		return nil
	}
	return groupOfService(ctx, svc)
}

// nodeChanged ignores Node updates that cannot change eligibility, such as
// heartbeats.
func nodeChanged(e event.UpdateEvent) bool {
	o, ok1 := e.ObjectOld.(*corev1.Node)
	n, ok2 := e.ObjectNew.(*corev1.Node)
	if !ok1 || !ok2 {
		return true
	}
	return o.Spec.Unschedulable != n.Spec.Unschedulable ||
		o.Spec.ProviderID != n.Spec.ProviderID ||
		!o.DeletionTimestamp.Equal(n.DeletionTimestamp) ||
		!maps.Equal(o.Labels, n.Labels) ||
		nodeReadyStatus(o) != nodeReadyStatus(n)
}

func nodeReadyStatus(n *corev1.Node) corev1.ConditionStatus {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status
		}
	}
	return corev1.ConditionUnknown
}
