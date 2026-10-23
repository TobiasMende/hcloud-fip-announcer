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
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fipannouncerv1alpha1 "github.com/TobiasMende/hcloud-fip-announcer/api/v1alpha1"
)

const (
	serviceGroupAnnotation = "fip-announcer.inocea.de/group"
	// publishedAnnotation lists the ingress addresses this controller wrote.
	publishedAnnotation = "fip-announcer.inocea.de/published"

	// ServiceGroupIndex indexes Services by the group named in their annotation.
	ServiceGroupIndex = "group"
)

// ServiceReconciler writes the addresses of a FloatingIPGroup into the status of its member Services.
// An empty Class handles annotated Services without a loadBalancerClass.
type ServiceReconciler struct {
	Client   client.Client
	Class    string
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=services/status,verbs=get;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=fip-announcer.inocea.de,resources=floatingipgroups,verbs=get;list;watch

func (r *ServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	svc := &corev1.Service{}
	if err := r.Client.Get(ctx, req.NamespacedName, svc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !r.handles(svc) {
		return ctrl.Result{}, nil
	}

	groupName := svc.Annotations[serviceGroupAnnotation]
	if groupName == "" {
		return ctrl.Result{}, releaseService(ctx, r.Client, r.Class, svc)
	}

	group := &fipannouncerv1alpha1.FloatingIPGroup{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: groupName}, group); err != nil {
		if apierrors.IsNotFound(err) {
			r.Recorder.Eventf(svc, corev1.EventTypeWarning, "UnknownGroup", "FloatingIPGroup %q does not exist", groupName)
			return ctrl.Result{}, releaseService(ctx, r.Client, r.Class, svc)
		}
		return ctrl.Result{}, err
	}
	if !group.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, releaseService(ctx, r.Client, r.Class, svc)
	}

	want := r.desiredIngress(svc, group)
	if r.Class == "" && hasForeignIngress(svc, publishedAddresses(svc)) {
		r.Recorder.Eventf(svc, corev1.EventTypeWarning, "ForeignStatus",
			"Service already carries a load balancer address not written by this controller; taking it over. Another default LoadBalancer implementation may be running")
	}
	addrs := make([]string, 0, len(want))
	for _, ing := range want {
		addrs = append(addrs, ing.IP)
	}
	return ctrl.Result{}, writeIngress(ctx, r.Client, svc, want, addrs)
}

func (r *ServiceReconciler) handles(svc *corev1.Service) bool {
	return handlesClass(svc, r.Class)
}

// handlesClass reports whether a Service is a LoadBalancer of the class; an
// empty class matches Services without a loadBalancerClass.
func handlesClass(svc *corev1.Service, class string) bool {
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		return false
	}
	if class == "" {
		return svc.Spec.LoadBalancerClass == nil
	}
	return svc.Spec.LoadBalancerClass != nil && *svc.Spec.LoadBalancerClass == class
}

func (r *ServiceReconciler) desiredIngress(svc *corev1.Service, group *fipannouncerv1alpha1.FloatingIPGroup) []corev1.LoadBalancerIngress {
	families := svc.Spec.IPFamilies
	if len(families) == 0 {
		families = []corev1.IPFamily{corev1.IPv4Protocol}
	}
	vip := corev1.LoadBalancerIPModeVIP
	var out []corev1.LoadBalancerIngress
	for _, family := range families {
		addr := group.Status.IPv4
		if family == corev1.IPv6Protocol {
			addr = group.Status.IPv6
		}
		if addr == "" {
			r.Recorder.Eventf(svc, corev1.EventTypeWarning, "NoAddress",
				"Group %q has no %s address for this Service", group.Name, family)
			continue
		}
		out = append(out, corev1.LoadBalancerIngress{IP: addr, IPMode: &vip})
	}
	return out
}

// releaseService removes the addresses this controller wrote. Only this
// controller writes Services of a named class, so their ingress is cleared;
// with class none only the addresses recorded as published are removed.
func releaseService(ctx context.Context, c client.Client, class string, svc *corev1.Service) error {
	var keep []corev1.LoadBalancerIngress
	if class == "" {
		published := publishedAddresses(svc)
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			if !slices.Contains(published, ing.IP) {
				keep = append(keep, ing)
			}
		}
	}
	return writeIngress(ctx, c, svc, keep, nil)
}

// writeIngress sets the ingress and records addrs as the addresses this
// controller published. The annotation is widened before the status write
// and narrowed after it, so it never misses an address that is in status.
func writeIngress(ctx context.Context, c client.Client, svc *corev1.Service, want []corev1.LoadBalancerIngress,
	addrs []string) error {
	if !ingressEqual(svc.Status.LoadBalancer.Ingress, want) {
		if err := setPublished(ctx, c, svc, union(publishedAddresses(svc), addrs)); err != nil {
			return err
		}
		patch := client.MergeFrom(svc.DeepCopy())
		svc.Status.LoadBalancer.Ingress = want
		if err := c.Status().Patch(ctx, svc, patch); err != nil {
			return fmt.Errorf("patch status of %s/%s: %w", svc.Namespace, svc.Name, err)
		}
	}
	return setPublished(ctx, c, svc, addrs)
}

func setPublished(ctx context.Context, c client.Client, svc *corev1.Service, addrs []string) error {
	value := strings.Join(addrs, ",")
	if svc.Annotations[publishedAnnotation] == value {
		return nil
	}
	patch := client.MergeFrom(svc.DeepCopy())
	if value == "" {
		delete(svc.Annotations, publishedAnnotation)
	} else {
		if svc.Annotations == nil {
			svc.Annotations = map[string]string{}
		}
		svc.Annotations[publishedAnnotation] = value
	}
	if err := c.Patch(ctx, svc, patch); err != nil {
		return fmt.Errorf("annotate %s/%s: %w", svc.Namespace, svc.Name, err)
	}
	return nil
}

func publishedAddresses(svc *corev1.Service) []string {
	var out []string
	for a := range strings.SplitSeq(svc.Annotations[publishedAnnotation], ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, s := range b {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func hasForeignIngress(svc *corev1.Service, owned []string) bool {
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if !slices.Contains(owned, ing.IP) {
			return true
		}
	}
	return false
}

func ingressEqual(a, b []corev1.LoadBalancerIngress) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].IP != b[i].IP || a[i].Hostname != b[i].Hostname {
			return false
		}
		if (a[i].IPMode == nil) != (b[i].IPMode == nil) || (a[i].IPMode != nil && *a[i].IPMode != *b[i].IPMode) {
			return false
		}
	}
	return true
}

// IndexServiceGroup registers the ServiceGroupIndex field index on Services.
func IndexServiceGroup(ctx context.Context, indexer client.FieldIndexer) error {
	return indexer.IndexField(ctx, &corev1.Service{}, ServiceGroupIndex, func(o client.Object) []string {
		if g := o.GetAnnotations()[serviceGroupAnnotation]; g != "" {
			return []string{g}
		}
		return nil
	})
}

// SetupWithManager registers the "group" field index on Services and the controller.
// The FloatingIPGroupReconciler relies on that index, so this runs first.
func (r *ServiceReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	if err := IndexServiceGroup(ctx, mgr.GetFieldIndexer()); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Service{}).
		Watches(&fipannouncerv1alpha1.FloatingIPGroup{}, handler.EnqueueRequestsFromMapFunc(r.servicesOfGroup)).
		Named("service").
		Complete(r)
}

func (r *ServiceReconciler) servicesOfGroup(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &corev1.ServiceList{}
	if err := r.Client.List(ctx, list, client.MatchingFields{ServiceGroupIndex: obj.GetName()}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}
