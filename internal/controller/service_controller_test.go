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
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	fipannouncerv1alpha1 "github.com/TobiasMende/hcloud-fip-announcer/api/v1alpha1"
)

var svcSeq atomic.Int32

var _ = Describe("Service binding reconciler", func() {
	const class = "fip-announcer.inocea.de/floating-ip"

	var (
		bg       = context.Background()
		recorder *record.FakeRecorder
	)

	BeforeEach(func() {
		recorder = record.NewFakeRecorder(10)
	})

	newGroup := func(v4, v6 string) string {
		name := fmt.Sprintf("grp-%d", svcSeq.Add(1))
		g := &fipannouncerv1alpha1.FloatingIPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: fipannouncerv1alpha1.FloatingIPGroupSpec{
				Location: "fsn1",
				IPv4:     &fipannouncerv1alpha1.FloatingIPRef{Name: name},
			},
		}
		Expect(k8sClient.Create(bg, g)).To(Succeed())
		g.Status.IPv4 = v4
		g.Status.IPv6 = v6
		Expect(k8sClient.Status().Update(bg, g)).To(Succeed())
		return name
	}

	newService := func(lbClass *string, group string, families ...corev1.IPFamily) *corev1.Service {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("svc-%d", svcSeq.Add(1)),
				Namespace: "default",
			},
			Spec: corev1.ServiceSpec{
				Type:              corev1.ServiceTypeLoadBalancer,
				LoadBalancerClass: lbClass,
				Ports:             []corev1.ServicePort{{Port: 80}},
				IPFamilies:        families,
				IPFamilyPolicy:    ptr.To(corev1.IPFamilyPolicyPreferDualStack),
			},
		}
		if group != "" {
			svc.Annotations = map[string]string{serviceGroupAnnotation: group}
		}
		Expect(k8sClient.Create(bg, svc)).To(Succeed())
		return svc
	}

	reconcile := func(class string, svc *corev1.Service) {
		r := &ServiceReconciler{Client: k8sClient, Class: class, Recorder: recorder}
		_, err := r.Reconcile(bg, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}})
		Expect(err).NotTo(HaveOccurred())
	}

	ingress := func(svc *corev1.Service) []corev1.LoadBalancerIngress {
		got := &corev1.Service{}
		Expect(k8sClient.Get(bg, types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}, got)).To(Succeed())
		return got.Status.LoadBalancer.Ingress
	}

	published := func(svc *corev1.Service) string {
		got := &corev1.Service{}
		Expect(k8sClient.Get(bg, types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}, got)).To(Succeed())
		return got.Annotations[publishedAnnotation]
	}

	vip := corev1.LoadBalancerIPModeVIP

	It("writes IPv4 ingress for class and annotation", func() {
		g := newGroup("203.0.113.10", "")
		svc := newService(new(class), g, corev1.IPv4Protocol)
		reconcile(class, svc)
		Expect(ingress(svc)).To(Equal([]corev1.LoadBalancerIngress{{IP: "203.0.113.10", IPMode: &vip}}))
		reconcile(class, svc)
		Expect(ingress(svc)).To(HaveLen(1))
	})

	It("ignores other classes", func() {
		g := newGroup("203.0.113.11", "")
		svc := newService(new("other.io/class"), g, corev1.IPv4Protocol)
		reconcile(class, svc)
		Expect(ingress(svc)).To(BeEmpty())
	})

	It("with class none handles class-less Services only with the annotation", func() {
		g := newGroup("203.0.113.12", "")
		annotated := newService(nil, g, corev1.IPv4Protocol)
		plain := newService(nil, "", corev1.IPv4Protocol)
		classed := newService(new(class), g, corev1.IPv4Protocol)
		reconcile("", annotated)
		reconcile("", plain)
		reconcile("", classed)
		Expect(ingress(annotated)).To(HaveLen(1))
		Expect(ingress(plain)).To(BeEmpty())
		Expect(ingress(classed)).To(BeEmpty())
	})

	It("warns on foreign status in class none and takes over", func() {
		g := newGroup("203.0.113.13", "")
		svc := newService(nil, g, corev1.IPv4Protocol)
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "198.51.100.1"}}
		Expect(k8sClient.Status().Update(bg, svc)).To(Succeed())
		reconcile("", svc)
		Expect(recorder.Events).To(Receive(ContainSubstring("ForeignStatus")))
		Expect(ingress(svc)).To(Equal([]corev1.LoadBalancerIngress{{IP: "203.0.113.13", IPMode: &vip}}))
	})

	It("writes only IPv4 and warns when IPv6 is requested on an IPv4-only group", func() {
		// the envtest API server is single-stack, so a dual-stack Service cannot be created
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "dual", Namespace: "default"},
			Spec:       corev1.ServiceSpec{IPFamilies: []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol}},
		}
		group := &fipannouncerv1alpha1.FloatingIPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "v4only"},
			Status:     fipannouncerv1alpha1.FloatingIPGroupStatus{IPv4: "203.0.113.14"},
		}
		r := &ServiceReconciler{Client: k8sClient, Class: class, Recorder: recorder}
		Expect(r.desiredIngress(svc, group)).To(Equal([]corev1.LoadBalancerIngress{{IP: "203.0.113.14", IPMode: &vip}}))
		Expect(recorder.Events).To(Receive(ContainSubstring("NoAddress")))
	})

	It("warns on an unknown group and clears the ingress of its class", func() {
		svc := newService(new(class), "does-not-exist", corev1.IPv4Protocol)
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "198.51.100.3"}}
		Expect(k8sClient.Status().Update(bg, svc)).To(Succeed())
		reconcile(class, svc)
		Expect(recorder.Events).To(Receive(ContainSubstring("UnknownGroup")))
		Expect(ingress(svc)).To(BeEmpty())
	})

	It("records the published addresses in an annotation", func() {
		g := newGroup("203.0.113.20", "")
		svc := newService(new(class), g, corev1.IPv4Protocol)
		reconcile(class, svc)
		Expect(published(svc)).To(Equal("203.0.113.20"))
	})

	It("clears the ingress when the group is deleted", func() {
		g := newGroup("203.0.113.21", "")
		svc := newService(new(class), g, corev1.IPv4Protocol)
		reconcile(class, svc)
		Expect(ingress(svc)).To(HaveLen(1))

		Expect(k8sClient.Delete(bg, &fipannouncerv1alpha1.FloatingIPGroup{ObjectMeta: metav1.ObjectMeta{Name: g}})).To(Succeed())
		reconcile(class, svc)
		Expect(ingress(svc)).To(BeEmpty())
		Expect(published(svc)).To(BeEmpty())
	})

	It("clears the ingress while the group is being deleted", func() {
		g := newGroup("203.0.113.22", "")
		grp := &fipannouncerv1alpha1.FloatingIPGroup{}
		Expect(k8sClient.Get(bg, types.NamespacedName{Name: g}, grp)).To(Succeed())
		grp.Finalizers = []string{"test.inocea.de/hold"}
		Expect(k8sClient.Update(bg, grp)).To(Succeed())
		svc := newService(new(class), g, corev1.IPv4Protocol)
		reconcile(class, svc)
		Expect(ingress(svc)).To(HaveLen(1))

		Expect(k8sClient.Delete(bg, grp)).To(Succeed())
		reconcile(class, svc)
		Expect(ingress(svc)).To(BeEmpty())

		Expect(k8sClient.Get(bg, types.NamespacedName{Name: g}, grp)).To(Succeed())
		grp.Finalizers = nil
		Expect(k8sClient.Update(bg, grp)).To(Succeed())
	})

	It("with class none clears only published addresses when the group is gone", func() {
		g := newGroup("203.0.113.23", "")
		svc := newService(nil, g, corev1.IPv4Protocol)
		reconcile("", svc)
		got := &corev1.Service{}
		Expect(k8sClient.Get(bg, types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}, got)).To(Succeed())
		got.Status.LoadBalancer.Ingress = append(got.Status.LoadBalancer.Ingress, corev1.LoadBalancerIngress{IP: "198.51.100.23"})
		Expect(k8sClient.Status().Update(bg, got)).To(Succeed())

		Expect(k8sClient.Delete(bg, &fipannouncerv1alpha1.FloatingIPGroup{ObjectMeta: metav1.ObjectMeta{Name: g}})).To(Succeed())
		reconcile("", svc)
		Expect(ingress(svc)).To(HaveExactElements(HaveField("IP", "198.51.100.23")))
		Expect(published(svc)).To(BeEmpty())
	})

	It("with class none follows a group IP change without a foreign-status warning", func() {
		g := newGroup("203.0.113.24", "")
		svc := newService(nil, g, corev1.IPv4Protocol)
		reconcile("", svc)
		Expect(published(svc)).To(Equal("203.0.113.24"))

		grp := &fipannouncerv1alpha1.FloatingIPGroup{}
		Expect(k8sClient.Get(bg, types.NamespacedName{Name: g}, grp)).To(Succeed())
		grp.Status.IPv4 = "203.0.113.25"
		Expect(k8sClient.Status().Update(bg, grp)).To(Succeed())
		reconcile("", svc)
		Expect(recorder.Events).NotTo(Receive(ContainSubstring("ForeignStatus")))
		Expect(ingress(svc)).To(Equal([]corev1.LoadBalancerIngress{{IP: "203.0.113.25", IPMode: &vip}}))
		Expect(published(svc)).To(Equal("203.0.113.25"))
	})

	It("with class none clears a stale address after a group IP change", func() {
		g := newGroup("203.0.113.26", "")
		svc := newService(nil, g, corev1.IPv4Protocol)
		reconcile("", svc)

		grp := &fipannouncerv1alpha1.FloatingIPGroup{}
		Expect(k8sClient.Get(bg, types.NamespacedName{Name: g}, grp)).To(Succeed())
		grp.Status.IPv4 = "203.0.113.27"
		Expect(k8sClient.Status().Update(bg, grp)).To(Succeed())
		got := &corev1.Service{}
		Expect(k8sClient.Get(bg, types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}, got)).To(Succeed())
		delete(got.Annotations, serviceGroupAnnotation)
		Expect(k8sClient.Update(bg, got)).To(Succeed())
		reconcile("", svc)
		Expect(ingress(svc)).To(BeEmpty())
	})

	It("clears owned ingress when the group has no address for the Service", func() {
		other := newGroup("203.0.113.17", "")
		empty := newGroup("", "")
		svc := newService(new(class), other, corev1.IPv4Protocol)
		reconcile(class, svc)
		Expect(ingress(svc)).To(HaveLen(1))

		got := &corev1.Service{}
		Expect(k8sClient.Get(bg, types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}, got)).To(Succeed())
		got.Annotations[serviceGroupAnnotation] = empty
		Expect(k8sClient.Update(bg, got)).To(Succeed())
		reconcile(class, svc)
		Expect(recorder.Events).To(Receive(ContainSubstring("NoAddress")))
		Expect(ingress(svc)).To(BeEmpty())
	})

	It("clears the status when the annotation is removed", func() {
		g := newGroup("203.0.113.15", "")
		svc := newService(new(class), g, corev1.IPv4Protocol)
		reconcile(class, svc)
		Expect(ingress(svc)).To(HaveLen(1))

		got := &corev1.Service{}
		Expect(k8sClient.Get(bg, types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}, got)).To(Succeed())
		delete(got.Annotations, serviceGroupAnnotation)
		Expect(k8sClient.Update(bg, got)).To(Succeed())
		reconcile(class, svc)
		Expect(ingress(svc)).To(BeEmpty())
	})

	It("with class none keeps foreign status when the annotation is removed", func() {
		newGroup("203.0.113.16", "")
		svc := newService(nil, "", corev1.IPv4Protocol)
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "198.51.100.2"}}
		Expect(k8sClient.Status().Update(bg, svc)).To(Succeed())
		reconcile("", svc)
		Expect(ingress(svc)).To(HaveLen(1))
	})
})
