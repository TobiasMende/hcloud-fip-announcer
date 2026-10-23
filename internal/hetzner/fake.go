package hetzner

import (
	"context"
	"fmt"
	"maps"
	"sync"
)

// Method names used as keys for SetErr and CallCount.
const (
	MethodFloatingIPByName = "FloatingIPByName"
	MethodCreateFloatingIP = "CreateFloatingIP"
	MethodAssign           = "Assign"
	MethodSetLabels        = "SetLabels"
	MethodDelete           = "Delete"
	MethodServer           = "Server"
)

// Fake is an in-memory Client for tests. SetErr injects an error per method
// name; CallCount counts invocations per method name.
type Fake struct {
	mu      sync.Mutex
	nextID  int64
	fips    map[int64]*FloatingIP
	servers map[int64]*Server
	errs    map[string]error
	calls   map[string]int
}

var _ Client = (*Fake)(nil)

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{
		nextID:  1000,
		fips:    map[int64]*FloatingIP{},
		servers: map[int64]*Server{},
		errs:    map[string]error{},
		calls:   map[string]int{},
	}
}

// AddServer registers a server.
func (f *Fake) AddServer(s Server) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.servers[s.ID] = &s
}

// AddFloatingIP registers a Floating IP.
func (f *Fake) AddFloatingIP(fip FloatingIP) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fips[fip.ID] = clone(&fip)
}

// FloatingIP returns a copy of the stored Floating IP, or nil.
func (f *Fake) FloatingIP(id int64) *FloatingIP {
	f.mu.Lock()
	defer f.mu.Unlock()
	return clone(f.fips[id])
}

// SetErr sets (or with nil clears) the injected error for a method.
func (f *Fake) SetErr(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.errs, method)
		return
	}
	f.errs[method] = err
}

// CallCount returns how often a method was called.
func (f *Fake) CallCount(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

// enter counts the call and returns the injected error. Callers hold f.mu.
func (f *Fake) enter(method string) error {
	f.calls[method]++
	return f.errs[method]
}

func (f *Fake) FloatingIPByName(_ context.Context, name string) (*FloatingIP, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter(MethodFloatingIPByName); err != nil {
		return nil, err
	}
	for _, fip := range f.fips {
		if fip.Name == name {
			return clone(fip), nil
		}
	}
	return nil, ErrNotFound
}

func (f *Fake) CreateFloatingIP(_ context.Context, opts CreateOpts) (*FloatingIP, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter(MethodCreateFloatingIP); err != nil {
		return nil, err
	}
	f.nextID++
	fip := &FloatingIP{
		ID:           f.nextID,
		Name:         opts.Name,
		Type:         opts.Type,
		Labels:       maps.Clone(opts.Labels),
		HomeLocation: opts.HomeLocation,
	}
	f.fips[fip.ID] = fip
	return clone(fip), nil
}

func (f *Fake) Assign(_ context.Context, fipID, serverID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter(MethodAssign); err != nil {
		return err
	}
	fip, ok := f.fips[fipID]
	if !ok {
		return ErrNotFound
	}
	if _, ok := f.servers[serverID]; !ok {
		return fmt.Errorf("server %d: %w", serverID, ErrNotFound)
	}
	fip.ServerID = serverID
	return nil
}

func (f *Fake) SetLabels(_ context.Context, fipID int64, labels map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter(MethodSetLabels); err != nil {
		return err
	}
	fip, ok := f.fips[fipID]
	if !ok {
		return ErrNotFound
	}
	fip.Labels = maps.Clone(labels)
	return nil
}

func (f *Fake) Delete(_ context.Context, fipID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter(MethodDelete); err != nil {
		return err
	}
	delete(f.fips, fipID)
	return nil
}

func (f *Fake) Server(_ context.Context, id int64) (*Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter(MethodServer); err != nil {
		return nil, err
	}
	s, ok := f.servers[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *s
	return &cp, nil
}

func clone(fip *FloatingIP) *FloatingIP {
	if fip == nil {
		return nil
	}
	cp := *fip
	cp.Labels = maps.Clone(fip.Labels)
	return &cp
}
