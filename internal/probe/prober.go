// Package probe actively checks the local pod endpoints of Floating IP member
// Services so a dead node is detected faster than EndpointSlice readiness.
package probe

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/TobiasMende/hcloud-fip-announcer/api/v1alpha1"
	"github.com/TobiasMende/hcloud-fip-announcer/internal/eligibility"
)

const (
	// HealthCheckAnnotation configures the probe of a Service.
	HealthCheckAnnotation = "fip-announcer.inocea.de/health-check"

	eventBuffer = 1024

	KindHTTP = "http"
	KindTCP  = "tcp"
)

// Check describes one probe: an HTTP GET expecting 2xx, or a TCP connect.
type Check struct {
	Kind string // "http" or "tcp"
	Port int
	Path string // http only
}

// Target is one (node, service) pair to probe on the pod IPs of its ready
// local endpoints.
type Target struct {
	Node    string
	Service string // "namespace/name"
	PodIPs  []net.IP
	Check   Check
}

// ParseCheck parses "http://:<port><path>" or "tcp://:<port>".
func ParseCheck(annotation string) (Check, error) {
	u, err := url.Parse(annotation)
	if err != nil {
		return Check{}, fmt.Errorf("invalid health check %q: %w", annotation, err)
	}
	if u.Scheme != KindHTTP && u.Scheme != KindTCP {
		return Check{}, fmt.Errorf("invalid health check %q: scheme must be http or tcp", annotation)
	}
	if u.Hostname() != "" || u.User != nil {
		return Check{}, fmt.Errorf("invalid health check %q: host must be empty", annotation)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return Check{}, fmt.Errorf("invalid health check %q: bad port", annotation)
	}
	c := Check{Kind: u.Scheme, Port: port}
	if u.Scheme == KindTCP {
		if u.Path != "" || u.RawQuery != "" {
			return Check{}, fmt.Errorf("invalid health check %q: tcp takes no path", annotation)
		}
		return c, nil
	}
	c.Path = u.RequestURI()
	return c, nil
}

// CheckFor returns the probe for a Service: the annotation if present, else a
// TCP connect to the first TCP endpoint port. ok is false when there is
// nothing to probe (e.g. UDP only) and readiness alone decides.
func CheckFor(svc corev1.Service, slice discoveryv1.EndpointSlice) (Check, bool, error) {
	if a, found := svc.Annotations[HealthCheckAnnotation]; found {
		c, err := ParseCheck(a)
		return c, err == nil, err
	}
	for _, p := range slice.Ports {
		if p.Port == nil || (p.Protocol != nil && *p.Protocol != corev1.ProtocolTCP) {
			continue
		}
		return Check{Kind: KindTCP, Port: int(*p.Port)}, true, nil
	}
	return Check{}, false, nil
}

var probeHealthy = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "fip_announcer_probe_healthy",
	Help: "1 if the probe of a (group, node, service) target is healthy, 0 if it is failing.",
}, []string{"group", "node", "service"})

func init() { metrics.Registry.MustRegister(probeHealthy) }

type targetKey struct {
	group string
	key   eligibility.NodeKey
}

type entry struct {
	target  Target
	cancel  context.CancelFunc
	known   bool
	healthy bool
	fails   int
}

// Prober probes targets on an interval. A target flips to failing after
// threshold consecutive failures and back to healthy on the first success.
// It only runs while the manager holds the leader lease.
type Prober struct {
	interval  time.Duration
	threshold int
	events    chan event.GenericEvent

	mu      sync.Mutex
	ctx     context.Context
	entries map[targetKey]*entry
}

// New returns a Prober. The per-probe timeout equals the interval.
func New(interval time.Duration, threshold int) *Prober {
	return &Prober{
		interval:  interval,
		threshold: threshold,
		events:    make(chan event.GenericEvent, eventBuffer),
		entries:   map[targetKey]*entry{},
	}
}

// Start runs the probe goroutines until ctx is cancelled.
func (p *Prober) Start(ctx context.Context) error {
	p.mu.Lock()
	p.ctx = ctx
	for k, e := range p.entries {
		p.launch(k, e)
	}
	p.mu.Unlock()

	<-ctx.Done()

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.entries {
		if e.cancel != nil {
			e.cancel()
		}
	}
	return nil
}

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (p *Prober) NeedLeaderElection() bool { return true }

// SetTargets replaces the targets of a group. Unchanged targets keep their
// state; removed ones stop and vanish from Results.
func (p *Prober) SetTargets(group string, targets []Target) {
	p.mu.Lock()
	defer p.mu.Unlock()

	wanted := map[targetKey]Target{}
	for _, t := range targets {
		wanted[targetKey{group, eligibility.NodeKey{Node: t.Node, Service: t.Service}}] = t
	}
	for k, e := range p.entries {
		if _, keep := wanted[k]; keep || k.group != group {
			continue
		}
		if e.cancel != nil {
			e.cancel()
		}
		probeHealthy.DeleteLabelValues(k.group, k.key.Node, k.key.Service)
		delete(p.entries, k)
	}
	for k, t := range wanted {
		if e, ok := p.entries[k]; ok {
			e.target = t
			continue
		}
		e := &entry{target: t}
		p.entries[k] = e
		p.launch(k, e)
	}
}

// launch starts the goroutine of an entry once the prober is running.
// The caller holds p.mu.
func (p *Prober) launch(k targetKey, e *entry) {
	if p.ctx == nil || e.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(p.ctx)
	e.cancel = cancel
	go p.loop(ctx, k, e)
}

// Results reports healthy (true) or failing (false) per target. Targets
// without a result yet are absent. If several groups probe the same
// (node, service), failing wins.
func (p *Prober) Results() map[eligibility.NodeKey]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[eligibility.NodeKey]bool{}
	for k, e := range p.entries {
		if !e.known {
			continue
		}
		if prev, ok := out[k.key]; ok {
			out[k.key] = prev && e.healthy
		} else {
			out[k.key] = e.healthy
		}
	}
	return out
}

// Events emits one event per result change (including the first result of a
// target), with the group as object. Sends never block; with the buffer full
// an event is dropped, which only delays the reconciler to its next resync.
func (p *Prober) Events() <-chan event.GenericEvent { return p.events }

func (p *Prober) loop(ctx context.Context, k targetKey, e *entry) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		p.mu.Lock()
		t := e.target
		p.mu.Unlock()

		ok := p.probeAny(ctx, t)
		if ctx.Err() != nil {
			return
		}
		p.record(ctx, k, e, ok)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Prober) record(ctx context.Context, k targetKey, e *entry, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	changed := false
	if ok {
		e.fails = 0
		changed = !e.known || !e.healthy
		e.known, e.healthy = true, true
	} else {
		e.fails++
		if e.fails >= p.threshold && (!e.known || e.healthy) {
			changed = true
			e.known, e.healthy = true, false
		}
	}
	if changed {
		v := 0.0
		if e.healthy {
			v = 1
		}
		probeHealthy.WithLabelValues(k.group, k.key.Node, k.key.Service).Set(v)
		p.emit(k.group)
	}
}

func (p *Prober) emit(group string) {
	ev := event.GenericEvent{Object: &v1alpha1.FloatingIPGroup{ObjectMeta: metav1.ObjectMeta{Name: group}}}
	select {
	case p.events <- ev:
	default:
	}
}

// probeAny reports whether any pod IP of the target passes.
func (p *Prober) probeAny(ctx context.Context, t Target) bool {
	results := make(chan bool, len(t.PodIPs))
	for _, ip := range t.PodIPs {
		go func() { results <- p.probeOne(ctx, ip, t.Check) }()
	}
	for range t.PodIPs {
		if <-results {
			return true
		}
	}
	return false
}

func (p *Prober) probeOne(ctx context.Context, ip net.IP, c Check) bool {
	ctx, cancel := context.WithTimeout(ctx, p.interval)
	defer cancel()
	addr := net.JoinHostPort(ip.String(), strconv.Itoa(c.Port))

	if c.Kind == KindTCP {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+c.Path, nil)
	if err != nil {
		return false
	}
	client := &http.Client{
		Transport:     &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}
