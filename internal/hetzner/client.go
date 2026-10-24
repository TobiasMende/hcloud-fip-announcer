// Package hetzner wraps the parts of the Hetzner Cloud API the announcer needs.
package hetzner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const (
	maxAttempts = 5
	baseBackoff = 500 * time.Millisecond
)

// ErrNotFound is returned when a Hetzner resource does not exist.
var ErrNotFound = errors.New("hetzner resource not found")

var (
	requestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "fip_announcer_hcloud_requests_total",
		Help: "Hetzner Cloud API requests by HTTP method and status code.",
	}, []string{"method", "code"})
	requestDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "fip_announcer_hcloud_request_duration_seconds",
		Help:    "Hetzner Cloud API request latency.",
		Buckets: prometheus.DefBuckets,
	})
)

func init() {
	metrics.Registry.MustRegister(requestsTotal, requestDuration)
}

// FloatingIP is a Hetzner Floating IP.
type FloatingIP struct {
	ID              int64
	Name            string
	Type            string // ipv4 or ipv6
	IP              net.IP
	Network         *net.IPNet
	ServerID        int64 // 0 means unassigned
	Labels          map[string]string
	HomeLocation    string
	NetworkZone     string // network zone of the home location
	DeleteProtected bool
}

// Server is a Hetzner server.
type Server struct {
	ID          int64
	Name        string
	Status      string
	Location    string
	NetworkZone string
}

// CreateOpts describes a Floating IP to create.
type CreateOpts struct {
	Name         string
	Type         string
	HomeLocation string
	Labels       map[string]string
}

// Client is the Hetzner API surface used by the controller.
type Client interface {
	FloatingIPByName(ctx context.Context, name string) (*FloatingIP, error)
	GetFloatingIP(ctx context.Context, id int64) (*FloatingIP, error)
	// AllFloatingIPs and AllServers list everything in the project (paged).
	AllFloatingIPs(ctx context.Context) ([]FloatingIP, error)
	AllServers(ctx context.Context) ([]Server, error)
	CreateFloatingIP(ctx context.Context, opts CreateOpts) (*FloatingIP, error)
	Assign(ctx context.Context, fipID, serverID int64) error
	// AssignAll issues every assignment before waiting for any of them and
	// returns the IDs that ended up on the server.
	AssignAll(ctx context.Context, serverID int64, fipIDs []int64) ([]int64, error)
	SetLabels(ctx context.Context, fipID int64, labels map[string]string) error
	SetDeleteProtection(ctx context.Context, fipID int64, on bool) error
	Delete(ctx context.Context, fipID int64) error
}

type client struct {
	api *hcloud.Client
}

// New returns a Client for the given API token. Extra options (for example
// hcloud.WithEndpoint in tests) are applied last.
func New(token string, opts ...hcloud.ClientOption) Client {
	return newWithBackoff(token, baseBackoff, opts...)
}

func newWithBackoff(token string, backoff time.Duration, opts ...hcloud.ClientOption) Client {
	all := make([]hcloud.ClientOption, 0, 4+len(opts))
	all = append(all,
		hcloud.WithToken(token),
		hcloud.WithApplication("hcloud-fip-announcer", ""),
		hcloud.WithRetryOpts(hcloud.RetryOpts{MaxRetries: 0}),
		hcloud.WithHTTPClient(&http.Client{
			Transport: &retryTransport{next: http.DefaultTransport, backoff: backoff},
			Timeout:   30 * time.Second,
		}),
	)
	return &client{api: hcloud.NewClient(append(all, opts...)...)}
}

// retryTransport retries 429 and 5xx responses with exponential backoff and
// records metrics for every attempt.
type retryTransport struct {
	next    http.RoundTripper
	backoff time.Duration
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		if body, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		_ = req.Body.Close()
	}
	delay := t.backoff
	for attempt := 1; ; attempt++ {
		r := req.Clone(req.Context())
		if body != nil {
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		start := time.Now()
		resp, err := t.next.RoundTrip(r)
		requestDuration.Observe(time.Since(start).Seconds())
		if err != nil {
			requestsTotal.WithLabelValues(req.Method, "error").Inc()
			return nil, err
		}
		requestsTotal.WithLabelValues(req.Method, strconv.Itoa(resp.StatusCode)).Inc()
		if !retryable(resp.StatusCode) || attempt == maxAttempts {
			return resp, nil
		}
		wait := delay
		if d, ok := rateLimitDelay(resp, time.Now()); ok {
			wait = d
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(wait):
		}
		delay *= 2
	}
}

const maxRetryAfter = 10 * time.Second

// rateLimitDelay returns the wait requested by a 429 response via Retry-After
// (seconds) or RateLimit-Reset (unix timestamp), capped at maxRetryAfter.
func rateLimitDelay(resp *http.Response, now time.Time) (time.Duration, bool) {
	if resp.StatusCode != http.StatusTooManyRequests {
		return 0, false
	}
	var d time.Duration
	if v := resp.Header.Get("Retry-After"); v != "" {
		secs, err := strconv.Atoi(v)
		if err != nil {
			return 0, false
		}
		d = time.Duration(secs) * time.Second
	} else if v := resp.Header.Get("RateLimit-Reset"); v != "" {
		ts, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, false
		}
		d = time.Unix(ts, 0).Sub(now)
	} else {
		return 0, false
	}
	return min(max(d, 0), maxRetryAfter), true
}

func retryable(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

func (c *client) FloatingIPByName(ctx context.Context, name string) (*FloatingIP, error) {
	f, _, err := c.api.FloatingIP.GetByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, ErrNotFound
	}
	return convertFIP(f), nil
}

func (c *client) CreateFloatingIP(ctx context.Context, opts CreateOpts) (*FloatingIP, error) {
	create := hcloud.FloatingIPCreateOpts{
		Type:   hcloud.FloatingIPType(opts.Type),
		Name:   &opts.Name,
		Labels: opts.Labels,
	}
	if opts.HomeLocation != "" {
		create.HomeLocation = &hcloud.Location{Name: opts.HomeLocation}
	}
	res, _, err := c.api.FloatingIP.Create(ctx, create)
	if err != nil {
		return nil, err
	}
	if err := c.waitAction(ctx, res.Action); err != nil {
		return nil, err
	}
	if err := c.setDeleteProtection(ctx, res.FloatingIP, true); err != nil {
		return nil, fmt.Errorf("enable delete protection: %w", err)
	}
	return c.refetch(ctx, res.FloatingIP.ID)
}

func (c *client) GetFloatingIP(ctx context.Context, id int64) (*FloatingIP, error) {
	return c.refetch(ctx, id)
}

func (c *client) AllFloatingIPs(ctx context.Context) ([]FloatingIP, error) {
	all, err := c.api.FloatingIP.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]FloatingIP, 0, len(all))
	for _, f := range all {
		out = append(out, *convertFIP(f))
	}
	return out, nil
}

func (c *client) AllServers(ctx context.Context) ([]Server, error) {
	all, err := c.api.Server.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Server, 0, len(all))
	for _, s := range all {
		srv, _ := convertServer(s)
		out = append(out, *srv)
	}
	return out, nil
}

// Assign assigns the Floating IP to the server. Already being assigned to that
// server counts as success.
func (c *client) Assign(ctx context.Context, fipID, serverID int64) error {
	_, err := c.AssignAll(ctx, serverID, []int64{fipID})
	return err
}

func (c *client) AssignAll(ctx context.Context, serverID int64, fipIDs []int64) ([]int64, error) {
	type started struct {
		id     int64
		action *hcloud.Action
	}
	var pending []started
	var done []int64
	var errs []error
	for _, id := range fipIDs {
		action, _, err := c.api.FloatingIP.Assign(ctx, &hcloud.FloatingIP{ID: id}, &hcloud.Server{ID: serverID})
		if err == nil {
			pending = append(pending, started{id, action})
			continue
		}
		if cur, getErr := c.refetch(ctx, id); getErr == nil && cur.ServerID == serverID {
			done = append(done, id)
			continue
		}
		errs = append(errs, err)
	}
	for _, p := range pending {
		if err := c.waitAction(ctx, p.action); err != nil {
			errs = append(errs, err)
			continue
		}
		done = append(done, p.id)
	}
	return done, errors.Join(errs...)
}

func (c *client) SetLabels(ctx context.Context, fipID int64, labels map[string]string) error {
	_, _, err := c.api.FloatingIP.Update(ctx, &hcloud.FloatingIP{ID: fipID}, hcloud.FloatingIPUpdateOpts{Labels: labels})
	return notFound(err)
}

func (c *client) SetDeleteProtection(ctx context.Context, fipID int64, on bool) error {
	return notFound(c.setDeleteProtection(ctx, &hcloud.FloatingIP{ID: fipID}, on))
}

// Delete disables delete protection, unassigns and deletes the Floating IP.
// A Floating IP that is already gone is not an error.
func (c *client) Delete(ctx context.Context, fipID int64) error {
	f, _, err := c.api.FloatingIP.GetByID(ctx, fipID)
	if err != nil {
		return err
	}
	if f == nil {
		return nil
	}
	if f.Protection.Delete {
		if err := c.setDeleteProtection(ctx, f, false); err != nil {
			return fmt.Errorf("disable delete protection: %w", err)
		}
	}
	if f.Server != nil {
		action, _, err := c.api.FloatingIP.Unassign(ctx, f)
		if err != nil {
			return fmt.Errorf("unassign: %w", err)
		}
		if err := c.waitAction(ctx, action); err != nil {
			return err
		}
	}
	_, err = c.api.FloatingIP.Delete(ctx, f)
	if hcloud.IsError(err, hcloud.ErrorCodeNotFound) {
		return nil
	}
	return err
}

func convertServer(s *hcloud.Server) (*Server, error) {
	if s == nil {
		return nil, ErrNotFound
	}
	out := &Server{ID: s.ID, Name: s.Name, Status: string(s.Status)}
	if s.Location != nil {
		out.Location = s.Location.Name
		out.NetworkZone = string(s.Location.NetworkZone)
	}
	return out, nil
}

func (c *client) setDeleteProtection(ctx context.Context, f *hcloud.FloatingIP, on bool) error {
	action, _, err := c.api.FloatingIP.ChangeProtection(ctx, f, hcloud.FloatingIPChangeProtectionOpts{Delete: &on})
	if err != nil {
		return err
	}
	return c.waitAction(ctx, action)
}

func (c *client) waitAction(ctx context.Context, a *hcloud.Action) error {
	if a == nil {
		return nil
	}
	return c.api.Action.WaitFor(ctx, a)
}

func (c *client) refetch(ctx context.Context, id int64) (*FloatingIP, error) {
	f, _, err := c.api.FloatingIP.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, ErrNotFound
	}
	return convertFIP(f), nil
}

func notFound(err error) error {
	if hcloud.IsError(err, hcloud.ErrorCodeNotFound) {
		return ErrNotFound
	}
	return err
}

func convertFIP(f *hcloud.FloatingIP) *FloatingIP {
	out := &FloatingIP{
		ID:      f.ID,
		Name:    f.Name,
		Type:    string(f.Type),
		IP:      f.IP,
		Network: f.Network,
		Labels:  f.Labels,

		DeleteProtected: f.Protection.Delete,
	}
	if f.Server != nil {
		out.ServerID = f.Server.ID
	}
	if f.HomeLocation != nil {
		out.HomeLocation = f.HomeLocation.Name
		out.NetworkZone = string(f.HomeLocation.NetworkZone)
	}
	return out
}
