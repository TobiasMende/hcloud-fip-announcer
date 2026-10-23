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
	"maps"
	"sync"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/TobiasMende/hcloud-fip-announcer/internal/hetzner"
)

// refreshBackoff is how long a failed Hetzner refresh blocks the next attempt.
const refreshBackoff = 10 * time.Second

// hetznerCache is a project-wide snapshot of Floating IPs and servers, shared
// by all groups. It costs one paged Floating IP list and one paged server list
// per poll interval, however many groups and nodes there are. A failed refresh
// keeps the last good snapshot and is not retried before refreshBackoff.
type hetznerCache struct {
	api hetzner.Client
	ttl time.Duration
	now func() time.Time

	// refreshMu makes concurrent reconciles share one refresh.
	refreshMu sync.Mutex

	mu   sync.Mutex
	fips map[string]hetzner.FloatingIP
	// gen counts putFIP writes; fipGen is the write generation per entry, so
	// a refresh keeps entries written while its list was in flight.
	gen         uint64
	fipGen      map[string]uint64
	servers     map[int64]hetzner.Server
	serverNames map[string]int64
	at          time.Time // last successful refresh, zero before the first
	failedAt    time.Time
	failure     error
}

func newHetznerCache(api hetzner.Client, ttl time.Duration, now func() time.Time) *hetznerCache {
	return &hetznerCache{api: api, ttl: ttl, now: now}
}

// refresh reloads the snapshot when it is older than the poll interval. An
// error means the snapshot is stale or missing (see ready).
func (c *hetznerCache) refresh(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.mu.Lock()
	now := c.now()
	fresh := !c.at.IsZero() && now.Sub(c.at) < c.ttl
	blocked := !c.failedAt.IsZero() && now.Sub(c.failedAt) < refreshBackoff
	failure := c.failure
	c.mu.Unlock()
	if fresh {
		return nil
	}
	if blocked {
		return failure
	}
	c.mu.Lock()
	startGen := c.gen
	c.mu.Unlock()

	fips, err := c.api.AllFloatingIPs(ctx)
	var servers []hetzner.Server
	if err == nil {
		servers, err = c.api.AllServers(ctx)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil && ctx.Err() != nil {
		// the caller gave up; that says nothing about the Hetzner API
		return fmt.Errorf("refresh Hetzner snapshot: %w", err)
	}
	if err != nil {
		c.failedAt, c.failure = c.now(), fmt.Errorf("refresh Hetzner snapshot: %w", err)
		logf.FromContext(ctx).Error(err, "Hetzner refresh failed; keeping the last snapshot", "retryIn", refreshBackoff)
		return c.failure
	}
	listed := make(map[string]hetzner.FloatingIP, len(fips))
	for _, f := range fips {
		listed[f.Name] = f
	}
	gens := map[string]uint64{}
	for name, g := range c.fipGen {
		if g > startGen {
			listed[name] = c.fips[name]
			gens[name] = g
		}
	}
	c.fips, c.fipGen = listed, gens
	c.servers = make(map[int64]hetzner.Server, len(servers))
	c.serverNames = make(map[string]int64, len(servers))
	for _, s := range servers {
		c.servers[s.ID] = s
		c.serverNames[s.Name] = s.ID
	}
	c.at, c.failedAt, c.failure = c.now(), time.Time{}, nil
	return nil
}

// ready reports whether a snapshot exists at all.
func (c *hetznerCache) ready() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.at.IsZero()
}

// fip returns a copy of the named Floating IP from the snapshot.
func (c *hetznerCache) fip(_ context.Context, name string) (*hetzner.FloatingIP, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.fips[name]
	if !ok {
		return nil, hetzner.ErrNotFound
	}
	return cloneFIP(&f), nil
}

// refreshFIP reads one Floating IP from the API, bypassing the snapshot.
func (c *hetznerCache) refreshFIP(ctx context.Context, id int64) (*hetzner.FloatingIP, error) {
	f, err := c.api.GetFloatingIP(ctx, id)
	if err != nil {
		return nil, err
	}
	c.putFIP(f)
	return cloneFIP(f), nil
}

// putFIP records a Floating IP the controller just read, created or changed.
func (c *hetznerCache) putFIP(f *hetzner.FloatingIP) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fips == nil {
		c.fips = map[string]hetzner.FloatingIP{}
		c.fipGen = map[string]uint64{}
	}
	c.gen++
	c.fips[f.Name] = *cloneFIP(f)
	c.fipGen[f.Name] = c.gen
}

func (c *hetznerCache) server(_ context.Context, id int64) (*hetzner.Server, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.servers[id]
	if !ok {
		return nil, hetzner.ErrNotFound
	}
	return &s, nil
}

func (c *hetznerCache) serverByName(ctx context.Context, name string) (*hetzner.Server, error) {
	c.mu.Lock()
	id, ok := c.serverNames[name]
	c.mu.Unlock()
	if !ok {
		return nil, hetzner.ErrNotFound
	}
	return c.server(ctx, id)
}

func cloneFIP(f *hetzner.FloatingIP) *hetzner.FloatingIP {
	cp := *f
	cp.Labels = maps.Clone(f.Labels)
	return &cp
}
