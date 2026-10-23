package eligibility

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	poolLabel = "pool"
	edgePool  = "edge"
)

func node(name string, mut ...func(*corev1.Node)) corev1.Node {
	n := corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
		}},
	}
	for _, m := range mut {
		m(&n)
	}
	return n
}

func svc(name string) corev1.Service {
	return corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name}}
}

func slice(ns, service string, nodes map[string]bool) discoveryv1.EndpointSlice {
	s := discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns,
		Name:      service + "-abc",
		Labels:    map[string]string{discoveryv1.LabelServiceName: service},
	}}
	for n, ready := range nodes {
		s.Endpoints = append(s.Endpoints, discoveryv1.Endpoint{
			NodeName:   new(n),
			Conditions: discoveryv1.EndpointConditions{Ready: new(ready)},
		})
	}
	return s
}

func base(nodes ...corev1.Node) Input {
	return Input{
		Nodes:    nodes,
		ServerOf: func(corev1.Node) (int64, bool) { return 1, true },
		ZoneOf:   func(int64) string { return "eu-central" },
		Zone:     "eu-central",
	}
}

func withService(in Input, nodes map[string]bool) Input {
	in.Services = []corev1.Service{svc("web")}
	in.Slices = []discoveryv1.EndpointSlice{slice("ns", "web", nodes)}
	return in
}

func assertReason(t *testing.T, got map[string]Reason, name string, want Reason) {
	t.Helper()
	r, ok := got[name]
	if !ok {
		t.Fatalf("node %s missing from result %v", name, got)
	}
	if r != want {
		t.Fatalf("node %s: reason %q, want %q", name, r, want)
	}
}

func TestCordonedNodeIneligible(t *testing.T) {
	in := withService(base(node("a", func(n *corev1.Node) { n.Spec.Unschedulable = true })), map[string]bool{"a": true})
	assertReason(t, Eligible(in), "a", Cordoned)
}

func TestNotReadyNodeIneligible(t *testing.T) {
	in := withService(base(node("a", func(n *corev1.Node) {
		n.Status.Conditions[0].Status = corev1.ConditionUnknown
	})), map[string]bool{"a": true})
	assertReason(t, Eligible(in), "a", NotReady)
}

func TestDeletingNodeIneligible(t *testing.T) {
	in := withService(base(node("a", func(n *corev1.Node) {
		n.DeletionTimestamp = new(metav1.Now())
	})), map[string]bool{"a": true})
	assertReason(t, Eligible(in), "a", Deleting)
}

func TestSelectorMismatch(t *testing.T) {
	in := withService(base(node("a"), node("b", func(n *corev1.Node) { n.Labels[poolLabel] = edgePool })), map[string]bool{"a": true, "b": true})
	in.Selector = labels.SelectorFromSet(labels.Set{poolLabel: edgePool})
	in.SelectorSet = true
	got := Eligible(in)
	assertReason(t, got, "a", SelectorMismatch)
	assertReason(t, got, "b", "")
}

func TestNeedsReadyEndpointForEveryService(t *testing.T) {
	in := base(node("a"), node("b"))
	in.Services = []corev1.Service{svc("web"), svc("dns")}
	in.Slices = []discoveryv1.EndpointSlice{
		slice("ns", "web", map[string]bool{"a": true}),
		slice("ns", "dns", map[string]bool{"b": true}),
	}
	got := Eligible(in)
	assertReason(t, got, "a", EndpointsNotReady)
	assertReason(t, got, "b", EndpointsNotReady)
	if len(eligibleNames(got)) != 0 {
		t.Fatalf("expected empty set, got %v", got)
	}
}

func TestFailingProbeIneligible(t *testing.T) {
	in := withService(base(node("a"), node("b")), map[string]bool{"a": true, "b": true})
	in.Probes = map[NodeKey]bool{{Node: "a", Service: "ns/web"}: false}
	got := Eligible(in)
	assertReason(t, got, "a", ProbeFailed)
	assertReason(t, got, "b", "")
}

func TestNodeWithoutServerIneligible(t *testing.T) {
	in := withService(base(node("a")), map[string]bool{"a": true})
	in.ServerOf = func(corev1.Node) (int64, bool) { return 0, false }
	assertReason(t, Eligible(in), "a", NoServer)
}

func TestOtherNetworkZoneIneligible(t *testing.T) {
	in := withService(base(node("a")), map[string]bool{"a": true})
	in.ZoneOf = func(int64) string { return "us-east" }
	assertReason(t, Eligible(in), "a", OtherZone)
}

func TestControlPlaneExcludedUnlessSelected(t *testing.T) {
	cp := node("cp", func(n *corev1.Node) { n.Labels["node-role.kubernetes.io/control-plane"] = "" })
	in := withService(base(cp), map[string]bool{"cp": true})
	assertReason(t, Eligible(in), "cp", ControlPlane)

	in.Selector = labels.SelectorFromSet(labels.Set{"node-role.kubernetes.io/control-plane": ""})
	in.SelectorSet = true
	assertReason(t, Eligible(in), "cp", "")
}

func TestNoServicesWithSelectorAllowsMatchingNodes(t *testing.T) {
	in := base(node("a", func(n *corev1.Node) { n.Labels[poolLabel] = edgePool }), node("b"))
	in.Selector = labels.SelectorFromSet(labels.Set{poolLabel: edgePool})
	in.SelectorSet = true
	got := Eligible(in)
	assertReason(t, got, "a", "")
	assertReason(t, got, "b", SelectorMismatch)
}

func TestNoServicesNoSelectorIsEmpty(t *testing.T) {
	got := Eligible(base(node("a")))
	if len(eligibleNames(got)) != 0 {
		t.Fatalf("expected empty set, got %v", got)
	}
}

func eligibleNames(m map[string]Reason) []string {
	var out []string
	for n, r := range m {
		if r == "" {
			out = append(out, n)
		}
	}
	return out
}

func TestNotReadyEndpointIneligible(t *testing.T) {
	in := withService(base(node("a")), map[string]bool{"a": false})
	assertReason(t, Eligible(in), "a", EndpointsNotReady)
}

func TestEndpointWithoutNodeNameIgnored(t *testing.T) {
	in := base(node("a"))
	in.Services = []corev1.Service{svc("web")}
	s := slice("ns", "web", nil)
	s.Endpoints = []discoveryv1.Endpoint{{Conditions: discoveryv1.EndpointConditions{Ready: new(true)}}}
	in.Slices = []discoveryv1.EndpointSlice{s}
	assertReason(t, Eligible(in), "a", EndpointsNotReady)
}

func TestSliceInOtherNamespaceIgnored(t *testing.T) {
	in := base(node("a"))
	in.Services = []corev1.Service{svc("web")}
	in.Slices = []discoveryv1.EndpointSlice{slice("other", "web", map[string]bool{"a": true})}
	assertReason(t, Eligible(in), "a", EndpointsNotReady)
}

func TestSliceWithoutServiceLabelIgnored(t *testing.T) {
	in := withService(base(node("a")), map[string]bool{"a": true})
	in.Slices[0].Labels = nil
	assertReason(t, Eligible(in), "a", EndpointsNotReady)
}

func TestMissingProbeResultIsNotFailing(t *testing.T) {
	in := withService(base(node("a")), map[string]bool{"a": true})
	in.Probes = map[NodeKey]bool{{Node: "b", Service: "ns/web"}: false}
	assertReason(t, Eligible(in), "a", "")
}
