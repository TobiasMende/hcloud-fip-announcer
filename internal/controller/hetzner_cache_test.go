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
	"testing"
	"time"

	"github.com/TobiasMende/hcloud-fip-announcer/internal/hetzner"
)

// blockingLister lists from the fake, then waits before returning, so a
// write can happen while the list result is in flight.
type blockingLister struct {
	*hetzner.Fake
	listed  chan struct{}
	proceed chan struct{}
}

func (b *blockingLister) AllFloatingIPs(ctx context.Context) ([]hetzner.FloatingIP, error) {
	fips, err := b.Fake.AllFloatingIPs(ctx)
	close(b.listed)
	<-b.proceed
	return fips, err
}

func TestRefreshKeepsEntriesWrittenWhileListing(t *testing.T) {
	fake := hetzner.NewFake()
	fake.AddFloatingIP(hetzner.FloatingIP{ID: 1, Name: "v4", Type: familyIPv4, ServerID: 10})
	api := &blockingLister{Fake: fake, listed: make(chan struct{}), proceed: make(chan struct{})}
	now := time.Unix(0, 0)
	c := newHetznerCache(api, 30*time.Second, func() time.Time { return now })

	done := make(chan error)
	go func() { done <- c.refresh(context.Background()) }()
	<-api.listed
	c.putFIP(&hetzner.FloatingIP{ID: 1, Name: "v4", Type: familyIPv4, ServerID: 20})
	c.putFIP(&hetzner.FloatingIP{ID: 2, Name: "new", Type: familyIPv4})
	close(api.proceed)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	f, err := c.fip(context.Background(), "v4")
	if err != nil || f.ServerID != 20 {
		t.Fatalf("listed entry overwrote a newer write: %+v %v", f, err)
	}
	if _, err := c.fip(context.Background(), "new"); err != nil {
		t.Fatalf("entry created while listing was dropped: %v", err)
	}

	now = now.Add(30 * time.Second)
	api.listed, api.proceed = make(chan struct{}), make(chan struct{})
	close(api.proceed)
	if err := c.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f, _ := c.fip(context.Background(), "v4"); f.ServerID != 10 {
		t.Fatalf("a later refresh must take the listed value again, got %d", f.ServerID)
	}
}

func TestRefreshCancelledByCallerDoesNotBackOff(t *testing.T) {
	fake := hetzner.NewFake()
	now := time.Unix(0, 0)
	c := newHetznerCache(fake, 30*time.Second, func() time.Time { return now })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake.SetErr(hetzner.MethodAllFloatingIPs, context.Canceled)
	if err := c.refresh(ctx); err == nil {
		t.Fatal("want error")
	}
	fake.SetErr(hetzner.MethodAllFloatingIPs, nil)
	if err := c.refresh(context.Background()); err != nil {
		t.Fatalf("a cancelled caller must not block the next refresh: %v", err)
	}
	if got := fake.CallCount(hetzner.MethodAllFloatingIPs); got != 2 {
		t.Fatalf("list calls = %d, want 2", got)
	}
}
