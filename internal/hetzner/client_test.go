package hetzner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const (
	fipID     = 5
	fipPath   = "/floating_ips/5"
	keyFIP    = "floating_ip"
	keyAction = "action"
	keyFIPs   = "floating_ips"
	zone      = "eu-central"
	fipName   = "web"
	keyName   = "name"
	keyZone   = "network_zone"
	typeV4    = "ipv4"
	locFSN    = "fsn1"
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
		if r.URL.Path == "/floating_ips" && r.URL.Query().Get("name") == fipName {
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
	if f.ID != 5 || f.ServerID != 9 || f.Type != typeV4 || f.HomeLocation != locFSN || f.Labels["a"] != "b" || f.IP.String() != "203.0.113.7" {
		t.Fatalf("unexpected conversion: %+v", f)
	}
}

func TestCreateEnablesDeleteProtection(t *testing.T) {
	var created map[string]any
	var protectionBody map[string]any
	a := newAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/floating_ips":
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
