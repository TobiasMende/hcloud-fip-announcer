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
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/TobiasMende/hcloud-fip-announcer/api/v1alpha1"
	"github.com/TobiasMende/hcloud-fip-announcer/internal/eligibility"
	"github.com/TobiasMende/hcloud-fip-announcer/internal/hetzner"
	"github.com/TobiasMende/hcloud-fip-announcer/internal/probe"
)

const (
	testClusterID = "test-cluster"
	testClass     = "fip-announcer.inocea.de/floating-ip"
	testZone      = "eu-central"
	testLocation  = "fsn1"
	testNamespace = "default"
)

var groupSeq atomic.Int64

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fakeProber struct {
	mu      sync.Mutex
	targets map[string][]probe.Target
	results map[eligibility.NodeKey]bool
}

func (p *fakeProber) SetTargets(group string, targets []probe.Target) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.targets[group] = targets
}

func (p *fakeProber) Results() map[eligibility.NodeKey]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return maps.Clone(p.results)
}

func (p *fakeProber) setResult(k eligibility.NodeKey, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.results[k] = ok
}

func (p *fakeProber) targetsOf(group string) []probe.Target {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.targets[group]
}

// env is one test's isolated world: its own nodes, Hetzner servers, member
// Service and group, all named after a unique sequence number.
type env struct {
	n        int64
	group    string
	svc      *corev1.Service
	fake     *hetzner.Fake
	clock    *fakeClock
	prober   *fakeProber
	recorder *record.FakeRecorder
	r        *FloatingIPGroupReconciler
}

func waitCached(obj client.Object) {
	GinkgoHelper()
	key := client.ObjectKeyFromObject(obj)
	got := obj.DeepCopyObject().(client.Object)
	Eventually(func() string {
		if err := cachedClient.Get(context.Background(), key, got); err != nil {
			return ""
		}
		return got.GetResourceVersion()
	}).Should(Equal(obj.GetResourceVersion()))
}

// newEnv creates the nodes (suffixes) and a member Service with ready
// endpoints on the nodes in withEndpoints.
func newEnv(suffixes []string, withEndpoints ...string) *env {
	GinkgoHelper()
	bg := context.Background()
	n := groupSeq.Add(1)
	e := &env{
		n:        n,
		group:    fmt.Sprintf("fipg-%d", n),
		fake:     hetzner.NewFake(),
		clock:    &fakeClock{t: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)},
		prober:   &fakeProber{targets: map[string][]probe.Target{}, results: map[eligibility.NodeKey]bool{}},
		recorder: record.NewFakeRecorder(1000),
	}
	for _, s := range suffixes {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: e.node(s)},
			Spec:       corev1.NodeSpec{ProviderID: fmt.Sprintf("hcloud://%d", e.serverID(s))},
		}
		Expect(k8sClient.Create(bg, node)).To(Succeed())
		node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
		Expect(k8sClient.Status().Update(bg, node)).To(Succeed())
		waitCached(node)
		e.fake.AddServer(hetzner.Server{ID: e.serverID(s), Name: e.node(s), Status: "running", Location: testLocation, NetworkZone: testZone})
	}

	e.svc = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        fmt.Sprintf("fipsvc-%d", n),
			Namespace:   testNamespace,
			Annotations: map[string]string{serviceGroupAnnotation: e.group},
		},
		Spec: corev1.ServiceSpec{
			Type:              corev1.ServiceTypeLoadBalancer,
			LoadBalancerClass: new(testClass),
			Ports:             []corev1.ServicePort{{Port: 80}},
		},
	}
	Expect(k8sClient.Create(bg, e.svc)).To(Succeed())
	waitCached(e.svc)

	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("fipsvc-%d-abc", n),
			Namespace: testNamespace,
			Labels:    map[string]string{discoveryv1.LabelServiceName: e.svc.Name},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Ports:       []discoveryv1.EndpointPort{{Port: ptr.To[int32](8080), Protocol: ptr.To(corev1.ProtocolTCP)}},
	}
	for i, s := range withEndpoints {
		slice.Endpoints = append(slice.Endpoints, discoveryv1.Endpoint{
			Addresses:  []string{fmt.Sprintf("10.%d.%d.%d", n/250, n%250, i+1)},
			NodeName:   new(e.node(s)),
			Conditions: discoveryv1.EndpointConditions{Ready: new(true)},
		})
	}
	Expect(k8sClient.Create(bg, slice)).To(Succeed())
	waitCached(slice)

	e.r = e.newReconciler()
	return e
}

// newReconciler returns a fresh controller instance, as after a restart.
func (e *env) newReconciler() *FloatingIPGroupReconciler {
	return &FloatingIPGroupReconciler{
		Client:       cachedClient,
		APIReader:    k8sClient,
		Scheme:       k8sClient.Scheme(),
		Hetzner:      e.fake,
		Recorder:     e.recorder,
		Prober:       e.prober,
		ClusterID:    testClusterID,
		Class:        testClass,
		AntiFlap:     30 * time.Second,
		PollInterval: 30 * time.Second,
		Now:          e.clock.Now,
	}
}

func (e *env) node(suffix string) string { return fmt.Sprintf("n%d-%s", e.n, suffix) }

func (e *env) serverID(suffix string) int64 { return e.n*100 + int64(suffix[0]-'a') + 1 }

func (e *env) fipName(family string) string { return fmt.Sprintf("%s-%s", e.group, family) }

func (e *env) ourLabels() map[string]string {
	return map[string]string{clusterIDLabel: testClusterID, groupLabel: e.group}
}

// addFIP registers an existing Hetzner Floating IP for the group.
func (e *env) addFIP(family string, serverID int64, labels map[string]string) {
	fip := hetzner.FloatingIP{
		ID:           e.n*100 + 50,
		Name:         e.fipName(family),
		Type:         family,
		ServerID:     serverID,
		Labels:       labels,
		HomeLocation: testLocation,
		NetworkZone:  testZone,
	}
	if family == familyIPv6 {
		fip.ID++
		fip.IP = net.ParseIP(fmt.Sprintf("2001:db8:%x::", e.n))
		fip.Network = &net.IPNet{IP: fip.IP, Mask: net.CIDRMask(64, 128)}
	} else {
		fip.IP = net.ParseIP(fmt.Sprintf("198.51.%d.%d", e.n/250, e.n%250)).To4()
	}
	e.fake.AddFloatingIP(fip)
}

func (e *env) fip(family string) *hetzner.FloatingIP {
	GinkgoHelper()
	f := e.fake.FloatingIPNamed(e.fipName(family))
	Expect(f).NotTo(BeNil())
	return f
}

func (e *env) createGroup(families ...string) {
	GinkgoHelper()
	e.createGroupWith(v1alpha1.DeletionPolicyRetain, families...)
}

func (e *env) createGroupWith(policy v1alpha1.DeletionPolicy, families ...string) {
	GinkgoHelper()
	g := &v1alpha1.FloatingIPGroup{
		ObjectMeta: metav1.ObjectMeta{Name: e.group},
		Spec:       v1alpha1.FloatingIPGroupSpec{Location: testLocation, DeletionPolicy: policy},
	}
	for _, f := range families {
		ref := &v1alpha1.FloatingIPRef{Name: e.fipName(f)}
		if f == familyIPv6 {
			g.Spec.IPv6 = ref
		} else {
			g.Spec.IPv4 = ref
		}
	}
	Expect(k8sClient.Create(context.Background(), g)).To(Succeed())
}

func (e *env) reconcile() (ctrl.Result, error) {
	return e.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: e.group}})
}

func (e *env) mustReconcile() ctrl.Result {
	GinkgoHelper()
	res, err := e.reconcile()
	Expect(err).NotTo(HaveOccurred())
	return res
}

func (e *env) get() *v1alpha1.FloatingIPGroup {
	GinkgoHelper()
	g := &v1alpha1.FloatingIPGroup{}
	Expect(k8sClient.Get(context.Background(), types.NamespacedName{Name: e.group}, g)).To(Succeed())
	return g
}

func (e *env) gone() bool {
	err := k8sClient.Get(context.Background(), types.NamespacedName{Name: e.group}, &v1alpha1.FloatingIPGroup{})
	return apierrors.IsNotFound(err)
}

// cordon cordons node "a", where every test places the group first.
func (e *env) cordon() {
	GinkgoHelper()
	node := &corev1.Node{}
	Expect(k8sClient.Get(context.Background(), types.NamespacedName{Name: e.node("a")}, node)).To(Succeed())
	node.Spec.Unschedulable = true
	Expect(k8sClient.Update(context.Background(), node)).To(Succeed())
	waitCached(node)
}

func (e *env) moveExternally(family string, suffix string) {
	GinkgoHelper()
	f := e.fip(family)
	f.ServerID = e.serverID(suffix)
	e.fake.AddFloatingIP(*f)
}

func (e *env) svcKey() string { return testNamespace + "/" + e.svc.Name }

func (e *env) events() []string {
	var out []string
	for {
		select {
		case ev := <-e.recorder.Events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func expectDegraded(g *v1alpha1.FloatingIPGroup, reason string) {
	GinkgoHelper()
	c := meta.FindStatusCondition(g.Status.Conditions, v1alpha1.ConditionDegraded)
	Expect(c).NotTo(BeNil())
	Expect(c.Status).To(Equal(metav1.ConditionTrue))
	Expect(c.Reason).To(Equal(reason))
	Expect(meta.IsStatusConditionTrue(g.Status.Conditions, v1alpha1.ConditionReady)).To(BeFalse())
}

func expectReady(g *v1alpha1.FloatingIPGroup) {
	GinkgoHelper()
	Expect(meta.IsStatusConditionTrue(g.Status.Conditions, v1alpha1.ConditionReady)).To(BeTrue())
	Expect(meta.IsStatusConditionFalse(g.Status.Conditions, v1alpha1.ConditionDegraded)).To(BeTrue())
}

func ipv6StatusAddr(f *hetzner.FloatingIP) string {
	ip := make(net.IP, net.IPv6len)
	copy(ip, f.Network.IP.To16())
	ip[15] = 1
	return ip.String()
}

var _ = Describe("FloatingIPGroup reconciler", func() {
	It("sets up with a manager next to the Service reconciler", func() {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:     k8sClient.Scheme(),
			Metrics:    metricsserver.Options{BindAddress: "0"},
			Controller: config.Controller{SkipNameValidation: new(true)},
		})
		Expect(err).NotTo(HaveOccurred())
		rec := record.NewFakeRecorder(1)
		Expect((&ServiceReconciler{Client: mgr.GetClient(), Class: testClass, Recorder: rec}).
			SetupWithManager(context.Background(), mgr)).To(Succeed())
		Expect((&FloatingIPGroupReconciler{
			Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Hetzner: hetzner.NewFake(), Recorder: rec,
			Prober: &fakeProber{}, ProbeEvents: make(chan event.GenericEvent),
		}).SetupWithManager(mgr)).To(Succeed())
	})

	It("TestAdoptsByNameAndLabels", func() {
		e := newEnv([]string{"a", "b"}, "a", "b")
		e.addFIP(familyIPv4, 0, nil)
		e.createGroup(familyIPv4)

		res := e.mustReconcile()
		Expect(res.RequeueAfter).To(Equal(30 * time.Second))

		f := e.fip(familyIPv4)
		Expect(f.Labels).To(Equal(e.ourLabels()))
		Expect(f.ServerID).To(Equal(e.serverID("a")))
		Expect(e.fake.CallCount(hetzner.MethodCreateFloatingIP)).To(BeZero())

		g := e.get()
		Expect(g.Finalizers).To(ContainElement(finalizerName))
		Expect(g.Status.IPv4).To(Equal(f.IP.String()))
		Expect(g.Status.IPv6).To(BeEmpty())
		Expect(g.Status.Node).To(Equal(e.node("a")))
		Expect(g.Status.Reason).To(Equal(v1alpha1.MoveReasonInitial))
		Expect(g.Status.Since).NotTo(BeNil())
		expectReady(g)
		Expect(testutil.ToFloat64(assignmentGauge.WithLabelValues(e.group, f.IP.String(), e.node("a")))).To(Equal(1.0))
		Expect(testutil.ToFloat64(eligibleNodesGauge.WithLabelValues(e.group))).To(Equal(2.0))
		Expect(testutil.ToFloat64(degradedGauge.WithLabelValues(e.group))).To(BeZero())
	})

	It("TestCreatesWhenMissing", func() {
		e := newEnv([]string{"a"}, "a")
		e.createGroup(familyIPv4, familyIPv6)

		e.mustReconcile()

		Expect(e.fake.CallCount(hetzner.MethodCreateFloatingIP)).To(Equal(2))
		v4, v6 := e.fip(familyIPv4), e.fip(familyIPv6)
		Expect(v4.Labels).To(Equal(e.ourLabels()))
		Expect(v6.Labels).To(Equal(e.ourLabels()))
		Expect(v4.HomeLocation).To(Equal(testLocation))
		Expect(v4.Type).To(Equal(familyIPv4))
		Expect(v6.Type).To(Equal(familyIPv6))
		Expect(v4.ServerID).To(Equal(e.serverID("a")))
		Expect(v6.ServerID).To(Equal(e.serverID("a")))

		g := e.get()
		Expect(g.Status.IPv4).To(Equal(v4.IP.String()))
		Expect(g.Status.IPv6).To(Equal(ipv6StatusAddr(v6)))
		Expect(g.Status.Node).To(Equal(e.node("a")))
		expectReady(g)
		Expect(e.events()).To(ContainElement(ContainSubstring("Created")))
	})

	It("TestRefusesFIPLabelledForOtherCluster", func() {
		e := newEnv([]string{"a"}, "a")
		e.addFIP(familyIPv4, 0, map[string]string{clusterIDLabel: "other", groupLabel: e.group})
		e.createGroup(familyIPv4)

		e.mustReconcile()

		g := e.get()
		expectDegraded(g, reasonRefused)
		Expect(g.Status.IPv4).To(BeEmpty())
		Expect(e.fake.CallCount(hetzner.MethodAssign)).To(BeZero())
		Expect(e.fake.CallCount(hetzner.MethodSetLabels)).To(BeZero())
		Expect(testutil.ToFloat64(degradedGauge.WithLabelValues(e.group))).To(Equal(1.0))

		By("the same cluster but another group")
		other := newEnv([]string{"a"}, "a")
		other.addFIP(familyIPv4, 0, map[string]string{clusterIDLabel: testClusterID, groupLabel: "another-group"})
		other.createGroup(familyIPv4)
		other.mustReconcile()
		expectDegraded(other.get(), reasonRefused)
		Expect(other.fake.CallCount(hetzner.MethodAssign)).To(BeZero())
		Expect(other.fake.CallCount(hetzner.MethodSetLabels)).To(BeZero())
	})

	It("TestRefusesFIPOnNonClusterServer", func() {
		e := newEnv([]string{"a"}, "a")
		foreign := e.n*100 + 99
		e.fake.AddServer(hetzner.Server{ID: foreign, Name: "not-a-node", Status: "running", NetworkZone: testZone})
		e.addFIP(familyIPv4, foreign, nil)
		e.createGroup(familyIPv4)

		e.mustReconcile()

		g := e.get()
		expectDegraded(g, string(v1alpha1.MoveReasonExternal))
		Expect(g.Status.Reason).To(Equal(v1alpha1.MoveReasonExternal))
		Expect(g.Status.Node).To(BeEmpty())
		Expect(e.fake.CallCount(hetzner.MethodAssign)).To(BeZero())
		Expect(e.fake.CallCount(hetzner.MethodSetLabels)).To(BeZero())
		Expect(e.fip(familyIPv4).ServerID).To(Equal(foreign))

		By("later reconciles keep the status stable")
		since := g.Status.Since
		e.clock.Advance(time.Minute)
		e.mustReconcile()
		Expect(e.get().Status.Since).To(Equal(since))
	})

	It("TestRestartKeepsEligibleHolder", func() {
		e := newEnv([]string{"a", "b"}, "a", "b")
		e.addFIP(familyIPv4, e.serverID("b"), e.ourLabels())
		e.addFIP(familyIPv6, e.serverID("b"), e.ourLabels())
		e.createGroup(familyIPv4, familyIPv6)

		e.mustReconcile()
		g := e.get()
		Expect(g.Status.Node).To(Equal(e.node("b")))
		expectReady(g)
		since := g.Status.Since

		By("a new controller instance with an empty cache")
		e.r = e.newReconciler()
		e.clock.Advance(time.Minute)
		e.mustReconcile()

		Expect(e.fake.CallCount(hetzner.MethodAssign)).To(BeZero())
		g = e.get()
		Expect(g.Status.Node).To(Equal(e.node("b")))
		Expect(g.Status.Since).To(Equal(since))
	})

	It("TestCordonMovesBothFamiliesTogether", func() {
		e := newEnv([]string{"a", "b"}, "a", "b")
		e.createGroup(familyIPv4, familyIPv6)
		e.mustReconcile()
		Expect(e.get().Status.Node).To(Equal(e.node("a")))
		e.events()

		e.cordon()
		e.clock.Advance(time.Second)
		e.mustReconcile()

		Expect(e.fip(familyIPv4).ServerID).To(Equal(e.serverID("b")))
		Expect(e.fip(familyIPv6).ServerID).To(Equal(e.serverID("b")))
		Expect(e.fake.CallCount(hetzner.MethodAssign)).To(Equal(4))
		g := e.get()
		Expect(g.Status.Node).To(Equal(e.node("b")))
		Expect(g.Status.Reason).To(Equal(v1alpha1.MoveReasonNodeCordoned))
		Expect(g.Status.Since.Time).To(BeTemporally("==", e.clock.Now()))
		expectReady(g)

		events := e.events()
		Expect(events).To(HaveLen(4)) // group, old node, new node, member Service
		Expect(events).To(HaveEach(ContainSubstring("Moved")))
		Expect(testutil.ToFloat64(movesTotal.WithLabelValues(e.group, string(v1alpha1.MoveReasonNodeCordoned)))).To(Equal(1.0))
		Expect(testutil.ToFloat64(lastMoveGauge.WithLabelValues(e.group))).To(Equal(float64(e.clock.Now().Unix())))
		Expect(testutil.ToFloat64(assignmentGauge.WithLabelValues(e.group, g.Status.IPv4, e.node("b")))).To(Equal(1.0))
		Expect(testutil.ToFloat64(assignmentGauge.WithLabelValues(e.group, g.Status.IPv6, e.node("b")))).To(Equal(1.0))
		Expect(testutil.CollectAndCount(assignmentGauge)).To(BeNumerically(">=", 2))
		Expect(testutil.ToFloat64(assignmentGauge.WithLabelValues(e.group, g.Status.IPv4, e.node("a")))).To(BeZero())
	})

	It("TestAssignErrorLeavesStatusUnchanged", func() {
		e := newEnv([]string{"a", "b"}, "a", "b")
		e.createGroup(familyIPv4, familyIPv6)
		e.mustReconcile()
		before := e.get().Status
		e.cordon()

		boom := errors.New("rate limited")
		e.fake.SetErrSequence(hetzner.MethodAssign, nil, boom, boom)

		By("IPv4 moves, IPv6 fails: nothing recorded")
		_, err := e.reconcile()
		Expect(err).To(MatchError(boom))
		Expect(e.fip(familyIPv4).ServerID).To(Equal(e.serverID("b")))
		Expect(e.fip(familyIPv6).ServerID).To(Equal(e.serverID("a")))
		after := e.get().Status
		Expect(after.Node).To(Equal(before.Node))
		Expect(after.Reason).To(Equal(before.Reason))
		Expect(after.Since).To(Equal(before.Since))

		By("second failure")
		_, err = e.reconcile()
		Expect(err).To(MatchError(boom))
		Expect(e.get().Status.Node).To(Equal(before.Node))

		By("success")
		e.mustReconcile()
		Expect(e.fip(familyIPv6).ServerID).To(Equal(e.serverID("b")))
		g := e.get()
		Expect(g.Status.Node).To(Equal(e.node("b")))
		Expect(g.Status.Reason).To(Equal(v1alpha1.MoveReasonNodeCordoned))
	})

	It("TestExternalMoveToEligibleNodeAccepted", func() {
		e := newEnv([]string{"a", "b"}, "a", "b")
		e.createGroup(familyIPv4)
		e.mustReconcile()
		e.moveExternally(familyIPv4, "b")

		e.clock.Advance(30 * time.Second)
		e.mustReconcile()

		Expect(e.fake.CallCount(hetzner.MethodAssign)).To(Equal(1))
		g := e.get()
		Expect(g.Status.Node).To(Equal(e.node("b")))
		Expect(g.Status.Reason).To(Equal(v1alpha1.MoveReasonExternal))
		expectReady(g)
	})

	It("TestExternalMoveToIneligibleNodeCorrected", func() {
		e := newEnv([]string{"a", "b", "c"}, "a", "c")
		e.createGroup(familyIPv4)
		e.mustReconcile()
		Expect(e.get().Status.Node).To(Equal(e.node("a")))
		e.moveExternally(familyIPv4, "b")

		e.clock.Advance(31 * time.Second)
		e.mustReconcile()

		Expect(e.fake.CallCount(hetzner.MethodAssign)).To(Equal(2))
		Expect(e.fip(familyIPv4).ServerID).To(Equal(e.serverID("a")))
		g := e.get()
		Expect(g.Status.Node).To(Equal(e.node("a")))
		Expect(g.Status.Reason).To(Equal(v1alpha1.MoveReasonEndpointsNotReady))
		expectReady(g)
	})

	It("TestServerOffMovesImmediately", func() {
		e := newEnv([]string{"a", "b"}, "a", "b")
		e.r.AntiFlap = time.Hour
		e.createGroup(familyIPv4)
		e.mustReconcile()
		e.fake.AddServer(hetzner.Server{ID: e.serverID("a"), Name: e.node("a"), Status: "off", NetworkZone: testZone})

		e.clock.Advance(30 * time.Second)
		e.mustReconcile()

		Expect(e.fip(familyIPv4).ServerID).To(Equal(e.serverID("b")))
		Expect(e.get().Status.Reason).To(Equal(v1alpha1.MoveReasonServerGone))
	})

	It("TestProbeFailureMovesAfterAntiFlap", func() {
		e := newEnv([]string{"a", "b"}, "a", "b")
		e.createGroup(familyIPv4)
		e.mustReconcile()

		targets := e.prober.targetsOf(e.group)
		Expect(targets).To(HaveLen(2))
		for _, t := range targets {
			Expect(t.Service).To(Equal(e.svcKey()))
			Expect(t.Check).To(Equal(probe.Check{Kind: probe.KindTCP, Port: 8080}))
			Expect(t.PodIPs).To(HaveLen(1))
		}

		e.prober.setResult(eligibility.NodeKey{Node: e.node("a"), Service: e.svcKey()}, false)
		e.clock.Advance(10 * time.Second)
		res := e.mustReconcile()
		Expect(e.fip(familyIPv4).ServerID).To(Equal(e.serverID("a")), "anti-flap holds the FIP")
		Expect(res.RequeueAfter).To(Equal(20 * time.Second))
		Expect(meta.IsStatusConditionTrue(e.get().Status.Conditions, v1alpha1.ConditionReady)).To(BeFalse())

		e.clock.Advance(20 * time.Second)
		e.mustReconcile()
		Expect(e.fip(familyIPv4).ServerID).To(Equal(e.serverID("b")))
		Expect(e.get().Status.Reason).To(Equal(v1alpha1.MoveReasonProbeFailed))
	})

	It("TestNoEligibleNodeDegradedFIPStays", func() {
		e := newEnv([]string{"a"}, "a")
		e.createGroup(familyIPv4)
		e.mustReconcile()
		e.cordon()
		e.events()

		e.mustReconcile()

		g := e.get()
		expectDegraded(g, reasonNoEligibleNode)
		Expect(g.Status.Node).To(Equal(e.node("a")))
		Expect(e.fip(familyIPv4).ServerID).To(Equal(e.serverID("a")))
		Expect(e.events()).To(ContainElement(ContainSubstring(reasonNoEligibleNode)))
		Expect(testutil.ToFloat64(degradedGauge.WithLabelValues(e.group))).To(Equal(1.0))
		Expect(testutil.ToFloat64(eligibleNodesGauge.WithLabelValues(e.group))).To(BeZero())
	})

	It("TestDeletionRetainKeepsFIP", func() {
		e := newEnv([]string{"a"}, "a")
		e.createGroup(familyIPv4)
		e.mustReconcile()
		Expect(k8sClient.Delete(context.Background(), e.get())).To(Succeed())

		e.mustReconcile()

		Expect(e.gone()).To(BeTrue())
		Expect(e.fake.CallCount(hetzner.MethodDelete)).To(BeZero())
		Expect(e.fip(familyIPv4).ServerID).To(Equal(e.serverID("a")))
		Expect(e.prober.targetsOf(e.group)).To(BeEmpty())
	})

	It("TestDeletionDeleteRemovesFIP", func() {
		e := newEnv([]string{"a"}, "a")
		e.createGroupWith(v1alpha1.DeletionPolicyDelete, familyIPv4, familyIPv6)
		e.mustReconcile()
		Expect(k8sClient.Delete(context.Background(), e.get())).To(Succeed())

		e.mustReconcile()

		Expect(e.gone()).To(BeTrue())
		Expect(e.fake.CallCount(hetzner.MethodDelete)).To(Equal(2))
		Expect(e.fake.FloatingIPNamed(e.fipName(familyIPv4))).To(BeNil())
		Expect(e.fake.FloatingIPNamed(e.fipName(familyIPv6))).To(BeNil())
	})

	It("TestPollingRespectsBudget", func() {
		e := newEnv([]string{"a", "b"}, "a", "b")
		e.createGroup(familyIPv4, familyIPv6)
		e.mustReconcile()

		reads := func() int {
			return e.fake.CallCount(hetzner.MethodFloatingIPByName) +
				e.fake.CallCount(hetzner.MethodServer) +
				e.fake.CallCount(hetzner.MethodServerByName)
		}
		afterFirst := reads()

		By("reconciles within the poll interval read nothing from Hetzner")
		for range 5 {
			e.clock.Advance(5 * time.Second)
			Expect(e.mustReconcile().RequeueAfter).To(Equal(30 * time.Second))
		}
		Expect(reads()).To(Equal(afterFirst))

		By("the poll refreshes each FIP and each candidate server once")
		e.clock.Advance(5 * time.Second)
		e.mustReconcile()
		e.mustReconcile()
		Expect(reads()).To(Equal(afterFirst + 4)) // 2 FIPs + 2 servers

		By("a move re-reads the FIPs right before assigning")
		e.cordon()
		fipReads := e.fake.CallCount(hetzner.MethodFloatingIPByName)
		e.mustReconcile()
		Expect(e.fake.CallCount(hetzner.MethodFloatingIPByName)).To(Equal(fipReads + 2))
		Expect(e.fip(familyIPv4).ServerID).To(Equal(e.serverID("b")))
	})
})
