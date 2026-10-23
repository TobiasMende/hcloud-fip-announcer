package hetzner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const (
	fipID           = 5
	floatingIPsPath = "/floating_ips"
	fipPath         = "/floating_ips/5"
	keyFIP          = "floating_ip"
	keyAction       = "action"
	keyFIPs         = "floating_ips"
	zone            = "eu-central"
	fipName         = "web"
	keyName         = "name"
	keyZone         = "network_zone"
	typeV4          = "ipv4"
	locFSN          = "fsn1"
	keyStatus       = "status"
	statusRunning   = "running"
	keyLocation     = "location"
)

type apiServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string
	handler  func(w http.ResponseWriter, r *http.Request) bool
}

func newAPI(t *testing.T, h func(w http.ResponseWriter, r *http.Request) bool) *apiServer {
	t.Helper()
	a := &apiServer{handler: h}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.requests = append(a.requests, r.Method+" "+r.URL.Path)
		a.mu.Unlock()
		if !a.handler(w, r) {
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(a.Close)
	return a
}

func (a *apiServer) seen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.requests...)
}

func (a *apiServer) client() Client {
	return newWithBackoff("tok", time.Millisecond, hcloud.WithEndpoint(a.URL))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": code}})
}

func okAction(id int) map[string]any {
	return map[string]any{"id": id, "status": "success", "progress": 100, "command": "x"}
}

func fipJSON(server int, protected bool) map[string]any {
	f := map[string]any{
		"id": fipID, keyName: fipName, "type": typeV4, "ip": "203.0.113.7",
		"labels":        map[string]string{"a": "b"},
		"protection":    map[string]bool{"delete": protected},
		"home_location": map[string]any{"id": 1, keyName: locFSN, keyZone: zone},
	}
	if server != 0 {
		f["server"] = server
	}
	return f
}

func TestFloatingIPByNameNotFound(t *testing.T) {
	a := newAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == floatingIPsPath && r.URL.Query().Get("name") == fipName {
			writeJSON(w, 200, map[string]any{keyFIPs: []any{}})
			return true
		}
		return false
	})
	_, err := a.client().FloatingIPByName(context.Background(), fipName)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestFloatingIPByNameFound(t *testing.T) {
	a := newAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		writeJSON(w, 200, map[string]any{keyFIPs: []any{fipJSON(9, true)}})
		return true
	})
	f, err := a.client().FloatingIPByName(context.Background(), fipName)
	if err != nil {
		t.Fatal(err)
	}
	if f.ID != 5 || f.ServerID != 9 || f.Type != typeV4 || f.HomeLocation != locFSN || f.Labels["a"] != "b" || f.IP.String() != "203.0.113.7" || f.NetworkZone != zone {
		t.Fatalf("unexpected conversion: %+v", f)
	}
}

func TestCreateEnablesDeleteProtection(t *testing.T) {
	var created map[string]any
	var protectionBody map[string]any
	a := newAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == floatingIPsPath:
			_ = json.NewDecoder(r.Body).Decode(&created)
			writeJSON(w, 201, map[string]any{keyFIP: fipJSON(0, false)})
		case r.Method == http.MethodPost && r.URL.Path == fipPath+"/actions/change_protection":
			_ = json.NewDecoder(r.Body).Decode(&protectionBody)
			writeJSON(w, 201, map[string]any{keyAction: okAction(1)})
		case r.Method == http.MethodGet && r.URL.Path == fipPath:
			writeJSON(w, 200, map[string]any{keyFIP: fipJSON(0, true)})
		default:
			return false
		}
		return true
	})
	f, err := a.client().CreateFloatingIP(context.Background(), CreateOpts{
		Name: fipName, Type: typeV4, HomeLocation: locFSN, Labels: map[string]string{"g": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.ID != 5 {
		t.Fatalf("unexpected fip %+v", f)
	}
	if created["name"] != fipName || created["type"] != typeV4 || created["home_location"] != locFSN {
		t.Fatalf("unexpected create body %v", created)
	}
	if protectionBody["delete"] != true {
		t.Fatalf("delete protection not enabled: %v", protectionBody)
	}
}

func TestDeleteDisablesProtectionFirst(t *testing.T) {
	a := newAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == fipPath:
			writeJSON(w, 200, map[string]any{keyFIP: fipJSON(9, true)})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/actions/change_protection"):
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["delete"] != false {
				t.Errorf("protection body %v", b)
			}
			writeJSON(w, 201, map[string]any{keyAction: okAction(1)})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/actions/unassign"):
			writeJSON(w, 201, map[string]any{keyAction: okAction(2)})
		case r.Method == http.MethodDelete && r.URL.Path == fipPath:
			w.WriteHeader(204)
		default:
			return false
		}
		return true
	})
	if err := a.client().Delete(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /floating_ips/5",
		"POST /floating_ips/5/actions/change_protection",
		"POST /floating_ips/5/actions/unassign",
		"DELETE /floating_ips/5",
	}
	if got := a.seen(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("request order\n got %v\nwant %v", got, want)
	}
}

func TestAssignAlreadyAssignedIsSuccess(t *testing.T) {
	a := newAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == fipPath+"/actions/assign":
			apiError(w, 422, "server_already_attached")
		case r.Method == http.MethodGet && r.URL.Path == fipPath:
			writeJSON(w, 200, map[string]any{keyFIP: fipJSON(9, true)})
		default:
			return false
		}
		return true
	})
	if err := a.client().Assign(context.Background(), 5, 9); err != nil {
		t.Fatal(err)
	}
}

func TestAssignErrorWhenHeldElsewhere(t *testing.T) {
	a := newAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch r.Method {
		case http.MethodPost:
			apiError(w, 422, "invalid_input")
		case http.MethodGet:
			writeJSON(w, 200, map[string]any{keyFIP: fipJSON(3, true)})
		default:
			return false
		}
		return true
	})
	if err := a.client().Assign(context.Background(), 5, 9); err == nil {
		t.Fatal("want error")
	}
}

func TestRetriesOn429And5xx(t *testing.T) {
	calls := 0
	a := newAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		calls++
		switch calls {
		case 1:
			apiError(w, 429, "rate_limit_exceeded")
		case 2:
			apiError(w, 503, "bad_gateway")
		default:
			writeJSON(w, 200, map[string]any{keyFIPs: []any{fipJSON(0, true)}})
		}
		return true
	})
	before := testutil.ToFloat64(requestsTotal.WithLabelValues(http.MethodGet, "429"))
	if _, err := a.client().FloatingIPByName(context.Background(), fipName); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("want 3 attempts, got %d", calls)
	}
	if got := testutil.ToFloat64(requestsTotal.WithLabelValues(http.MethodGet, "429")) - before; got != 1 {
		t.Fatalf("429 counter delta %v", got)
	}
}

func TestRetryGivesUpAfterFiveAttempts(t *testing.T) {
	calls := 0
	a := newAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		calls++
		apiError(w, 500, "server_error")
		return true
	})
	if _, err := a.client().FloatingIPByName(context.Background(), fipName); err == nil {
		t.Fatal("want error")
	}
	if calls != 5 {
		t.Fatalf("want 5 attempts, got %d", calls)
	}
}

func TestClientErrorsNotRetried(t *testing.T) {
	calls := 0
	a := newAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		calls++
		apiError(w, 401, "unauthorized")
		return true
	})
	if _, err := a.client().FloatingIPByName(context.Background(), fipName); err == nil {
		t.Fatal("want error")
	}
	if calls != 1 {
		t.Fatalf("want 1 attempt, got %d", calls)
	}
}

func TestServer(t *testing.T) {
	a := newAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/servers/9":
			writeJSON(w, 200, map[string]any{"server": map[string]any{
				"id": 9, keyName: "w1", "status": "running",
				"datacenter": map[string]any{"location": map[string]any{keyName: locFSN, keyZone: zone}},
				"location":   map[string]any{keyName: locFSN, keyZone: zone},
			}})
		case "/servers/10":
			apiError(w, 404, "not_found")
		default:
			return false
		}
		return true
	})
	s, err := a.client().Server(context.Background(), 9)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "w1" || s.Status != "running" || s.Location != locFSN || s.NetworkZone != zone {
		t.Fatalf("unexpected %+v", s)
	}
	if _, err := a.client().Server(context.Background(), 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestServerByName(t *testing.T) {
	a := newAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/servers" {
			return false
		}
		servers := []any{}
		if r.URL.Query().Get(keyName) == "w1" {
			servers = append(servers, map[string]any{
				"id": 9, keyName: "w1", keyStatus: "off",
				keyLocation: map[string]any{keyName: locFSN, keyZone: zone},
			})
		}
		writeJSON(w, 200, map[string]any{"servers": servers})
		return true
	})
	s, err := a.client().ServerByName(context.Background(), "w1")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != 9 || s.Status != "off" || s.NetworkZone != zone {
		t.Fatalf("unexpected %+v", s)
	}
	if _, err := a.client().ServerByName(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestFakeExtensions(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	f.AddServer(Server{ID: 1, Name: "n1", NetworkZone: zone})
	if s, err := f.ServerByName(ctx, "n1"); err != nil || s.ID != 1 {
		t.Fatalf("ServerByName: %+v %v", s, err)
	}
	if _, err := f.ServerByName(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	v4, _ := f.CreateFloatingIP(ctx, CreateOpts{Name: "a", Type: typeV4, HomeLocation: locFSN})
	v6, _ := f.CreateFloatingIP(ctx, CreateOpts{Name: "b", Type: "ipv6", HomeLocation: locFSN})
	if v4.IP.To4() == nil || v4.NetworkZone != zone {
		t.Fatalf("v4 not allocated: %+v", v4)
	}
	if v6.IP.To4() != nil || v6.Network == nil || v6.Network.IP.String() != v6.IP.String() {
		t.Fatalf("v6 not allocated: %+v", v6)
	}
	boom := errors.New("boom")
	f.SetErrSequence(MethodAssign, nil, boom)
	if err := f.Assign(ctx, v4.ID, 1); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := f.Assign(ctx, v6.ID, 1); !errors.Is(err, boom) {
		t.Fatalf("second call: %v", err)
	}
	if err := f.Assign(ctx, v6.ID, 1); err != nil {
		t.Fatalf("sequence exhausted: %v", err)
	}
}

func TestSetLabels(t *testing.T) {
	var body map[string]any
	a := newAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPut && r.URL.Path == fipPath {
			_ = json.NewDecoder(r.Body).Decode(&body)
			writeJSON(w, 200, map[string]any{keyFIP: fipJSON(0, true)})
			return true
		}
		return false
	})
	if err := a.client().SetLabels(context.Background(), 5, map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(body["labels"]) != "map[k:v]" {
		t.Fatalf("body %v", body)
	}
}

func TestFakeBehaviour(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	f.AddServer(Server{ID: 1, Name: "n1"})
	if _, err := f.FloatingIPByName(ctx, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	fip, err := f.CreateFloatingIP(ctx, CreateOpts{Name: "x", Type: typeV4})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Assign(ctx, fip.ID, 1); err != nil {
		t.Fatal(err)
	}
	if got := f.FloatingIP(fip.ID).ServerID; got != 1 {
		t.Fatalf("server %d", got)
	}
	f.SetErr(MethodAssign, errors.New("boom"))
	if err := f.Assign(ctx, fip.ID, 1); err == nil || f.CallCount(MethodAssign) != 2 {
		t.Fatalf("injected error / call count wrong: %v %d", err, f.CallCount(MethodAssign))
	}
	if err := f.Delete(ctx, fip.ID); err != nil {
		t.Fatal(err)
	}
	if f.FloatingIP(fip.ID) != nil {
		t.Fatal("not deleted")
	}
}

func TestRetryAfterHonoredAndCapped(t *testing.T) {
	now := time.Unix(1000, 0)
	mk := func(h, v string) *http.Response {
		r := &http.Response{StatusCode: 429, Header: http.Header{}}
		r.Header.Set(h, v)
		return r
	}
	cases := []struct {
		name string
		resp *http.Response
		want time.Duration
		ok   bool
	}{
		{"retry-after", mk("Retry-After", "3"), 3 * time.Second, true},
		{"capped", mk("Retry-After", "600"), maxRetryAfter, true},
		{"reset", mk("RateLimit-Reset", "1004"), 4 * time.Second, true},
		{"reset in past", mk("RateLimit-Reset", "900"), 0, true},
		{"none", &http.Response{StatusCode: 429, Header: http.Header{}}, 0, false},
		{"not 429", &http.Response{StatusCode: 503, Header: http.Header{"Retry-After": {"3"}}}, 0, false},
	}
	for _, c := range cases {
		got, ok := rateLimitDelay(c.resp, now)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got %v,%v want %v,%v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestRetryAfterUsedByTransport(t *testing.T) {
	calls := 0
	a := newAPI(t, func(w http.ResponseWriter, _ *http.Request) bool {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "1")
			apiError(w, 429, "rate_limit_exceeded")
		} else {
			writeJSON(w, 200, map[string]any{keyFIPs: []any{fipJSON(0, true)}})
		}
		return true
	})
	start := time.Now()
	if _, err := a.client().FloatingIPByName(context.Background(), fipName); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Fatal("Retry-After not honored")
	}
}

func TestRetriedPostResendsIdenticalBody(t *testing.T) {
	var bodies []string
	a := newAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodPut {
			return false
		}
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(bodies) == 1 {
			apiError(w, 502, "bad_gateway")
		} else {
			writeJSON(w, 200, map[string]any{keyFIP: fipJSON(0, true)})
		}
		return true
	})
	if err := a.client().SetLabels(context.Background(), fipID, map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0] == "" || bodies[0] != bodies[1] {
		t.Fatalf("bodies %q", bodies)
	}
}

func TestCreateWithoutHomeLocationRejectedBeforeRequest(t *testing.T) {
	a := newAPI(t, func(http.ResponseWriter, *http.Request) bool { return false })
	_, err := a.client().CreateFloatingIP(context.Background(), CreateOpts{Name: fipName, Type: typeV4})
	if err == nil || !strings.Contains(err.Error(), "HomeLocation") {
		t.Fatalf("want HomeLocation validation error, got %v", err)
	}
	if n := len(a.seen()); n != 0 {
		t.Fatalf("%d requests sent", n)
	}
}

func TestListsAndGetByID(t *testing.T) {
	a := newAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case floatingIPsPath:
			writeJSON(w, 200, map[string]any{keyFIPs: []any{fipJSON(9, true)}})
		case fipPath:
			writeJSON(w, 200, map[string]any{keyFIP: fipJSON(9, true)})
		case "/servers":
			writeJSON(w, 200, map[string]any{"servers": []any{map[string]any{
				"id": 9, keyName: "w1", keyStatus: statusRunning,
				keyLocation: map[string]any{keyName: locFSN, keyZone: zone},
			}}})
		default:
			return false
		}
		return true
	})
	ctx := context.Background()
	fips, err := a.client().AllFloatingIPs(ctx)
	if err != nil || len(fips) != 1 || fips[0].ServerID != 9 || fips[0].NetworkZone != zone {
		t.Fatalf("AllFloatingIPs: %+v %v", fips, err)
	}
	servers, err := a.client().AllServers(ctx)
	if err != nil || len(servers) != 1 || servers[0].Name != "w1" || servers[0].NetworkZone != zone {
		t.Fatalf("AllServers: %+v %v", servers, err)
	}
	f, err := a.client().GetFloatingIP(ctx, fipID)
	if err != nil || f.ID != fipID {
		t.Fatalf("GetFloatingIP: %+v %v", f, err)
	}
}

func TestAssignAllIssuesEveryAssignBeforeWaiting(t *testing.T) {
	a := newAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/actions/assign"):
			id := 1
			if strings.Contains(r.URL.Path, "/6/") {
				id = 2
			}
			writeJSON(w, 201, map[string]any{keyAction: map[string]any{"id": id, keyStatus: statusRunning, "progress": 0, "command": "x"}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/actions"):
			ids := r.URL.Query()["id"]
			actions := make([]any, 0, len(ids))
			for _, id := range ids {
				n, _ := strconv.Atoi(id)
				actions = append(actions, okAction(n))
			}
			writeJSON(w, 200, map[string]any{"actions": actions})
		default:
			return false
		}
		return true
	})
	done, err := a.client().AssignAll(context.Background(), 9, []int64{5, 6})
	if err != nil || len(done) != 2 {
		t.Fatalf("done %v err %v", done, err)
	}
	seen := a.seen()
	if len(seen) < 3 || !strings.HasPrefix(seen[0], "POST") || !strings.HasPrefix(seen[1], "POST") {
		t.Fatalf("both assigns must be issued before waiting: %v", seen)
	}
}

func TestFakeListsAndAssignAll(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	f.AddServer(Server{ID: 1, Name: "n1"})
	v4, _ := f.CreateFloatingIP(ctx, CreateOpts{Name: "a", Type: typeV4, HomeLocation: locFSN})
	v6, _ := f.CreateFloatingIP(ctx, CreateOpts{Name: "b", Type: "ipv6", HomeLocation: locFSN})
	boom := errors.New("boom")
	f.SetErrSequence(MethodAssign, nil, boom)
	done, err := f.AssignAll(ctx, 1, []int64{v4.ID, v6.ID})
	if !errors.Is(err, boom) || len(done) != 1 || done[0] != v4.ID {
		t.Fatalf("done %v err %v", done, err)
	}
	if f.FloatingIP(v4.ID).ServerID != 1 || f.FloatingIP(v6.ID).ServerID != 0 {
		t.Fatal("partial assignment not applied")
	}
	fips, _ := f.AllFloatingIPs(ctx)
	servers, _ := f.AllServers(ctx)
	got, _ := f.GetFloatingIP(ctx, v6.ID)
	if len(fips) != 2 || len(servers) != 1 || got.Name != "b" {
		t.Fatalf("lists %d %d get %+v", len(fips), len(servers), got)
	}
	if f.CallCount(MethodAllFloatingIPs) != 1 || f.CallCount(MethodAllServers) != 1 || f.CallCount(MethodGetFloatingIP) != 1 {
		t.Fatal("call counts")
	}
	if _, err := f.GetFloatingIP(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
