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
	"sync"
	"time"

	"github.com/TobiasMende/hcloud-fip-announcer/internal/hetzner"
)

// hetznerCache keeps Hetzner reads for one poll interval so reconciles
// triggered by cluster events cost no API requests. "Not found" is cached
// too; other errors are not.
type hetznerCache struct {
	api hetzner.Client
	ttl time.Duration
	now func() time.Time

	mu          sync.Mutex
	fips        map[string]cached[hetzner.FloatingIP]
	servers     map[int64]cached[hetzner.Server]
	serverNames map[string]cached[hetzner.Server]
}

type cached[T any] struct {
	val *T
	err error
	at  time.Time
}

func newHetznerCache(api hetzner.Client, ttl time.Duration, now func() time.Time) *hetznerCache {
	return &hetznerCache{
		api:         api,
		ttl:         ttl,
		now:         now,
		fips:        map[string]cached[hetzner.FloatingIP]{},
		servers:     map[int64]cached[hetzner.Server]{},
		serverNames: map[string]cached[hetzner.Server]{},
	}
}

func lookup[K comparable, T any](c *hetznerCache, m map[K]cached[T], key K, fetch func() (*T, error)) (*T, error) {
	c.mu.Lock()
	e, ok := m[key]
	c.mu.Unlock()
	if ok && c.now().Sub(e.at) < c.ttl {
		return e.val, e.err
	}
	v, err := fetch()
	if err != nil && !errors.Is(err, hetzner.ErrNotFound) {
		return nil, err
	}
	c.mu.Lock()
	m[key] = cached[T]{val: v, err: err, at: c.now()}
	c.mu.Unlock()
	return v, err
}

// fip returns the named Floating IP, from the cache while it is fresh.
func (c *hetznerCache) fip(ctx context.Context, name string) (*hetzner.FloatingIP, error) {
	return lookup(c, c.fips, name, func() (*hetzner.FloatingIP, error) { return c.api.FloatingIPByName(ctx, name) })
}

// refreshFIP always reads the Floating IP from the API.
func (c *hetznerCache) refreshFIP(ctx context.Context, name string) (*hetzner.FloatingIP, error) {
	c.mu.Lock()
	delete(c.fips, name)
	c.mu.Unlock()
	return c.fip(ctx, name)
}

// putFIP records a Floating IP the controller just created or changed,
// keeping the time of the last read so the poll schedule is unchanged.
func (c *hetznerCache) putFIP(f *hetzner.FloatingIP) {
	c.mu.Lock()
	defer c.mu.Unlock()
	at := c.now()
	if e, ok := c.fips[f.Name]; ok {
		at = e.at
	}
	c.fips[f.Name] = cached[hetzner.FloatingIP]{val: f, at: at}
}

func (c *hetznerCache) server(ctx context.Context, id int64) (*hetzner.Server, error) {
	return lookup(c, c.servers, id, func() (*hetzner.Server, error) { return c.api.Server(ctx, id) })
}

func (c *hetznerCache) serverByName(ctx context.Context, name string) (*hetzner.Server, error) {
	return lookup(c, c.serverNames, name, func() (*hetzner.Server, error) { return c.api.ServerByName(ctx, name) })
}
