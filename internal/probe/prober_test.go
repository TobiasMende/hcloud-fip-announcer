package probe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/TobiasMende/hcloud-fip-announcer/internal/eligibility"
)

const (
	interval  = 30 * time.Millisecond
	threshold = 3
)

const testService = "ns/svc"

var loopback = net.ParseIP("127.0.0.1")

func startProber(t *testing.T) *Prober {
	t.Helper()
	p := New(interval, threshold)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = p.Start(ctx) }()
	return p
}

func portOf(t *testing.T, s *httptest.Server) int {
	t.Helper()
	_, port, err := net.SplitHostPort(s.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(port)
	return n
}

func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func key(node string) eligibility.NodeKey {
	return eligibility.NodeKey{Node: node, Service: testService}
}

func httpTarget(node string, port int, ips ...net.IP) Target {
	return Target{Node: node, Service: testService, PodIPs: ips, Check: Check{Kind: KindHTTP, Port: port, Path: "/"}}
}

func tcpTarget(node string, port int, ips ...net.IP) Target {
	return Target{Node: node, Service: testService, PodIPs: ips, Check: Check{Kind: KindTCP, Port: port}}
}

func waitResult(t *testing.T, p *Prober, k eligibility.NodeKey, want bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := p.Results()[k]; ok && v == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("result for %v never became %v (got %v)", k, want, p.Results())
}

func TestParseCheck(t *testing.T) {
	good := map[string]Check{
		"http://:8080/ping": {Kind: KindHTTP, Port: 8080, Path: "/ping"},
		"http://:80":        {Kind: KindHTTP, Port: 80, Path: "/"},
		"http://:80/a?b=c":  {Kind: KindHTTP, Port: 80, Path: "/a?b=c"},
		"tcp://:3478":       {Kind: KindTCP, Port: 3478},
	}
	for in, want := range good {
		got, err := ParseCheck(in)
		if err != nil || got != want {
			t.Errorf("ParseCheck(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "8080", "udp://:53", "ftp://:21", "http://host:80/", "http://:/x", "http://:abc/",
		"http://:0/", "http://:65536/", "tcp://:80/path", "tcp://:", "tcp://host:80",
	} {
		if _, err := ParseCheck(in); err == nil {
			t.Errorf("ParseCheck(%q) succeeded, want error", in)
		}
	}
}

func TestHealthyAfterSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	p := startProber(t)
	p.SetTargets("g", []Target{httpTarget("n1", portOf(t, srv), loopback)})
	waitResult(t, p, key("n1"), true)
}

func TestNoResultBeforeFirstProbe(t *testing.T) {
	p := New(time.Hour, threshold)
	p.SetTargets("g", []Target{tcpTarget("n1", 1, loopback)})
	if len(p.Results()) != 0 {
		t.Fatalf("expected no results, got %v", p.Results())
	}
}

func TestNon2xxFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	p := startProber(t)
	p.SetTargets("g", []Target{httpTarget("n1", portOf(t, srv), loopback)})
	waitResult(t, p, key("n1"), false)
}

func TestFailingOnlyAfterThreshold(t *testing.T) {
	var hits, failFrom, failUntil atomic.Int32 // fail for hit numbers in (failFrom, failUntil]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := hits.Add(1)
		if n > failFrom.Load() && n <= failUntil.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	p := startProber(t)
	p.SetTargets("g", []Target{httpTarget("n1", portOf(t, srv), loopback)})
	waitResult(t, p, key("n1"), true)

	start := hits.Load()
	failFrom.Store(start)
	failUntil.Store(start + threshold - 1)
	for hits.Load() < start+threshold+3 {
		if !p.Results()[key("n1")] {
			t.Fatal("failing before threshold")
		}
		time.Sleep(time.Millisecond)
	}

	failFrom.Store(hits.Load())
	failUntil.Store(1 << 30)
	waitResult(t, p, key("n1"), false)
	failUntil.Store(0)
	waitResult(t, p, key("n1"), true)
}

func TestEventOnlyOnFlip(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	p := startProber(t)
	p.SetTargets("grp", []Target{httpTarget("n1", portOf(t, srv), loopback)})

	expectEvent := func() {
		t.Helper()
		select {
		case ev := <-p.Events():
			if ev.Object.GetName() != "grp" {
				t.Fatalf("event for %q, want grp", ev.Object.GetName())
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no event")
		}
	}
	expectNone := func() {
		t.Helper()
		select {
		case ev := <-p.Events():
			t.Fatalf("unexpected event %v", ev.Object.GetName())
		case <-time.After(10 * interval):
		}
	}

	expectEvent() // first result: healthy
	expectNone()  // steady state
	fail.Store(true)
	expectEvent() // healthy -> failing
	expectNone()  // still failing
	fail.Store(false)
	expectEvent() // failing -> healthy
	expectNone()
}

func TestNodeHealthyIfAnyLocalPodPasses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	p := startProber(t)
	// 127.0.0.2 is not served (the listener binds 127.0.0.1), so only the second IP passes.
	p.SetTargets("g", []Target{
		tcpTarget("good", portOf(t, srv), net.ParseIP("127.0.0.2"), loopback),
		tcpTarget("bad", closedPort(t), loopback),
	})
	waitResult(t, p, key("bad"), false)
	if v := p.Results()[key("good")]; !v {
		t.Fatalf("node with one passing pod must be healthy: %v", p.Results())
	}
}

func TestTimeoutCountsAsFailure(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	p := startProber(t)
	p.SetTargets("g", []Target{httpTarget("n1", portOf(t, srv), loopback)})
	waitResult(t, p, key("n1"), false)
}

func TestRemovedTargetDisappears(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	var hits atomic.Int32
	counter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer counter.Close()

	p := startProber(t)
	p.SetTargets("g", []Target{
		tcpTarget("n1", portOf(t, srv), loopback),
		httpTarget("n2", portOf(t, counter), loopback),
	})
	waitResult(t, p, key("n1"), true)
	waitResult(t, p, key("n2"), true)

	p.SetTargets("g", []Target{tcpTarget("n1", portOf(t, srv), loopback)})
	if _, ok := p.Results()[key("n2")]; ok {
		t.Fatal("removed target still in results")
	}
	time.Sleep(3 * interval)
	before := hits.Load()
	time.Sleep(5 * interval)
	if hits.Load() != before {
		t.Fatal("removed target is still probed")
	}
	p.SetTargets("g", nil)
	if len(p.Results()) != 0 {
		t.Fatalf("results not empty: %v", p.Results())
	}
}

func TestNoProbingBeforeStart(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()
	p := New(interval, threshold)
	p.SetTargets("g", []Target{httpTarget("n1", portOf(t, srv), loopback)})
	time.Sleep(5 * interval)
	if hits.Load() != 0 {
		t.Fatal("probed before Start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = p.Start(ctx); close(done) }()
	waitResult(t, p, key("n1"), true)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
	time.Sleep(3 * interval)
	before := hits.Load()
	time.Sleep(5 * interval)
	if hits.Load() != before {
		t.Fatal("probing continues after cancel")
	}
}

func TestNeedLeaderElection(t *testing.T) {
	if !New(interval, threshold).NeedLeaderElection() {
		t.Fatal("prober must run only on the leader")
	}
}

func TestDefaultTCPCheckFromServicePort(t *testing.T) {
	svc := corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"}}
	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	slice := discoveryv1.EndpointSlice{Ports: []discoveryv1.EndpointPort{
		{Protocol: &udp, Port: ptr.To[int32](3478)},
		{Protocol: &tcp, Port: ptr.To[int32](8080)},
	}}
	c, ok, err := CheckFor(svc, slice)
	if err != nil || !ok || c != (Check{Kind: KindTCP, Port: 8080}) {
		t.Fatalf("got %+v ok=%v err=%v", c, ok, err)
	}

	udpOnly := discoveryv1.EndpointSlice{Ports: []discoveryv1.EndpointPort{{Protocol: &udp, Port: ptr.To[int32](3478)}}}
	if _, ok, err := CheckFor(svc, udpOnly); ok || err != nil {
		t.Fatalf("udp-only: ok=%v err=%v, want no probe", ok, err)
	}

	svc.Annotations = map[string]string{"fip-announcer.inocea.de/health-check": "http://:9000/healthz"}
	c, ok, err = CheckFor(svc, udpOnly)
	if err != nil || !ok || c != (Check{Kind: KindHTTP, Port: 9000, Path: "/healthz"}) {
		t.Fatalf("annotation: got %+v ok=%v err=%v", c, ok, err)
	}

	svc.Annotations["fip-announcer.inocea.de/health-check"] = "bogus"
	if _, _, err := CheckFor(svc, slice); err == nil {
		t.Fatal("invalid annotation must error")
	}
}

func TestRedirectCountsAsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			return
		}
		http.Redirect(w, r, "/ok", http.StatusFound)
	}))
	defer srv.Close()
	p := startProber(t)
	p.SetTargets("g", []Target{httpTarget("n1", portOf(t, srv), loopback)})
	waitResult(t, p, key("n1"), false)
}

func TestSetTargetsKeepsFailureCounter(t *testing.T) {
	var p *Prober
	var port atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		p.SetTargets("g", []Target{httpTarget("n1", int(port.Load()), loopback)})
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	port.Store(int32(portOf(t, srv)))
	p = startProber(t)
	p.SetTargets("g", []Target{httpTarget("n1", int(port.Load()), loopback)})
	waitResult(t, p, key("n1"), false)
}
