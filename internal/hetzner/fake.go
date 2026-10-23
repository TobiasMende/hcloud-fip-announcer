package hetzner

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
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
	MethodServerByName     = "ServerByName"
	MethodGetFloatingIP    = "GetFloatingIP"
	MethodAllFloatingIPs   = "AllFloatingIPs"
	MethodAllServers       = "AllServers"
)

const zoneEUCentral = "eu-central"

var locationZones = map[string]string{
	"fsn1": zoneEUCentral, "nbg1": zoneEUCentral, "hel1": zoneEUCentral,
	"ash": "us-east", "hil": "us-west", "sin": "ap-southeast",
}

// Fake is an in-memory Client for tests. SetErr injects an error per method
// name; CallCount counts invocations per method name.
type Fake struct {
	mu      sync.Mutex
	nextID  int64
	fips    map[int64]*FloatingIP
	servers map[int64]*Server
	errs    map[string]error
	seqs    map[string][]error
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
		seqs:    map[string][]error{},
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

// FloatingIPNamed returns a copy of the named Floating IP, or nil, without
// counting a call.
func (f *Fake) FloatingIPNamed(name string) *FloatingIP {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, fip := range f.fips {
		if fip.Name == name {
			return clone(fip)
		}
	}
	return nil
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

// SetErrSequence makes the next calls of a method return errs in order (nil
// means success); afterwards SetErr applies again.
func (f *Fake) SetErrSequence(method string, errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seqs[method] = append([]error(nil), errs...)
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
	if seq := f.seqs[method]; len(seq) > 0 {
		f.seqs[method] = seq[1:]
		return seq[0]
	}
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
		NetworkZone:  locationZones[opts.HomeLocation],
	}
	n := byte(f.nextID % 256)
	if opts.Type == "ipv6" {
		fip.IP = net.IP{0x20, 0x01, 0x0d, 0xb8, 0, n, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		fip.Network = &net.IPNet{IP: fip.IP, Mask: net.CIDRMask(64, 128)}
	} else {
		fip.IP = net.IPv4(203, 0, 113, n).To4()
		fip.Network = &net.IPNet{IP: fip.IP, Mask: net.CIDRMask(32, 32)}
	}
	f.fips[fip.ID] = fip
	return clone(fip), nil
}

func (f *Fake) GetFloatingIP(_ context.Context, id int64) (*FloatingIP, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter(MethodGetFloatingIP); err != nil {
		return nil, err
	}
	fip, ok := f.fips[id]
	if !ok {
		return nil, ErrNotFound
	}
	return clone(fip), nil
}

func (f *Fake) AllFloatingIPs(_ context.Context) ([]FloatingIP, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter(MethodAllFloatingIPs); err != nil {
		return nil, err
	}
	out := make([]FloatingIP, 0, len(f.fips))
	for _, fip := range f.fips {
		out = append(out, *clone(fip))
	}
	slices.SortFunc(out, func(a, b FloatingIP) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

func (f *Fake) AllServers(_ context.Context) ([]Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter(MethodAllServers); err != nil {
		return nil, err
	}
	out := make([]Server, 0, len(f.servers))
	for _, s := range f.servers {
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b Server) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

// AssignAll counts one MethodAssign call per Floating IP.
func (f *Fake) AssignAll(_ context.Context, serverID int64, fipIDs []int64) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var done []int64
	var errs []error
	for _, id := range fipIDs {
		if err := f.assign(id, serverID); err != nil {
			errs = append(errs, err)
			continue
		}
		done = append(done, id)
	}
	return done, errors.Join(errs...)
}

func (f *Fake) Assign(_ context.Context, fipID, serverID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.assign(fipID, serverID)
}

// assign is one counted assignment. Callers hold f.mu.
func (f *Fake) assign(fipID, serverID int64) error {
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

func (f *Fake) ServerByName(_ context.Context, name string) (*Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter(MethodServerByName); err != nil {
		return nil, err
	}
	for _, s := range f.servers {
		if s.Name == name {
			cp := *s
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func clone(fip *FloatingIP) *FloatingIP {
	if fip == nil {
		return nil
	}
	cp := *fip
	cp.Labels = maps.Clone(fip.Labels)
	return &cp
}
