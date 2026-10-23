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
	"fmt"
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"

	"github.com/TobiasMende/hcloud-fip-announcer/api/v1alpha1"
	"github.com/TobiasMende/hcloud-fip-announcer/internal/hetzner"
)

// resolveFIPs adopts or creates every requested Floating IP. A non-empty
// refusal names a Floating IP the group must not touch.
func (r *FloatingIPGroupReconciler) resolveFIPs(ctx context.Context, group *v1alpha1.FloatingIPGroup) ([]groupFIP, string, error) {
	fips := requestedFIPs(group)
	for i := range fips {
		name := fips[i].ref.Name
		f, err := r.hcache().fip(ctx, name)
		if errors.Is(err, hetzner.ErrNotFound) {
			// The snapshot can be older than a Floating IP created since.
			if f, err = r.Hetzner.FloatingIPByName(ctx, name); err == nil {
				r.hcache().putFIP(f)
			}
		}
		if errors.Is(err, hetzner.ErrNotFound) {
			f, err = r.Hetzner.CreateFloatingIP(ctx, hetzner.CreateOpts{
				Name: name, Type: fips[i].family, HomeLocation: group.Spec.Location, Labels: r.ownLabels(group),
			})
			if err != nil {
				return nil, "", fmt.Errorf("create floating IP %s: %w", name, err)
			}
			r.hcache().putFIP(f)
			r.Recorder.Eventf(group, corev1.EventTypeNormal, "Created", "Created Floating IP %s (%s)", name, f.IP)
		}
		if err != nil {
			return nil, "", err
		}
		switch {
		case f.Type != fips[i].family:
			return nil, fmt.Sprintf("Floating IP %s is %s, not %s", name, f.Type, fips[i].family), nil
		case f.Labels[clusterIDLabel] != "" && f.Labels[clusterIDLabel] != r.ClusterID:
			return nil, fmt.Sprintf("Floating IP %s belongs to cluster %q", name, f.Labels[clusterIDLabel]), nil
		case f.Labels[groupLabel] != "" && f.Labels[groupLabel] != group.Name:
			return nil, fmt.Sprintf("Floating IP %s belongs to group %q", name, f.Labels[groupLabel]), nil
		}
		fips[i].fip = f
	}
	return fips, "", nil
}

func (r *FloatingIPGroupReconciler) ownLabels(group *v1alpha1.FloatingIPGroup) map[string]string {
	return map[string]string{clusterIDLabel: r.ClusterID, groupLabel: group.Name}
}

// ensureLabels labels adopted Floating IPs with the cluster and group.
func (r *FloatingIPGroupReconciler) ensureLabels(ctx context.Context, group *v1alpha1.FloatingIPGroup, fips []groupFIP) error {
	own := r.ownLabels(group)
	for i := range fips {
		f := fips[i].fip
		if f.Labels[clusterIDLabel] == own[clusterIDLabel] && f.Labels[groupLabel] == own[groupLabel] {
			continue
		}
		labels := maps.Clone(f.Labels)
		if labels == nil {
			labels = map[string]string{}
		}
		maps.Copy(labels, own)
		if err := r.Hetzner.SetLabels(ctx, f.ID, labels); err != nil {
			return fmt.Errorf("label floating IP %s: %w", f.Name, err)
		}
		cp := *f
		cp.Labels = labels
		r.hcache().putFIP(&cp)
		fips[i].fip = &cp
		r.Recorder.Eventf(group, corev1.EventTypeNormal, "Adopted", "Adopted Floating IP %s (%s)", f.Name, f.IP)
	}
	return nil
}

// ensureProtection enables delete protection on the group's Floating IPs.
// Callers label them first, so every one carries this cluster and group.
func (r *FloatingIPGroupReconciler) ensureProtection(ctx context.Context, fips []groupFIP) error {
	for i := range fips {
		f := fips[i].fip
		if f.DeleteProtected {
			continue
		}
		if err := r.Hetzner.SetDeleteProtection(ctx, f.ID, true); err != nil {
			return fmt.Errorf("protect floating IP %s: %w", f.Name, err)
		}
		cp := *f
		cp.DeleteProtected = true
		r.hcache().putFIP(&cp)
		fips[i].fip = &cp
	}
	return nil
}

// holder is where the group's FIPs are according to Hetzner.
type holder struct {
	node       string // cluster node holding the FIPs, "" if unassigned
	server     int64
	serverGone bool // the holder's server is deleted or powered off
	nodeGone   bool // the holder's Node object is gone
	foreign    string
}

// alignedNode is the holder if every FIP is on it, else "".
func (h holder) alignedNode(fips []groupFIP) string {
	for _, f := range fips {
		if h.server == 0 || f.fip.ServerID != h.server {
			return ""
		}
	}
	return h.node
}

// findHolder resolves the Hetzner assignment to a cluster node. When the
// FIPs are split (an interrupted move), the node in status wins, so the move
// is completed or undone as one unit. This also covers a leader change in the
// middle of a move: the new leader has no pending move in memory, but finds
// the split through Hetzner and repairs it from status.node. Only an
// interrupted initial assignment (status.node still empty) is then recorded
// as External, because nothing tells the new leader it was its predecessor.
func (r *FloatingIPGroupReconciler) findHolder(ctx context.Context, group *v1alpha1.FloatingIPGroup, snap *snapshot,
	fips []groupFIP) (holder, error) {
	var h holder
	for _, f := range fips {
		sid := f.fip.ServerID
		if sid == 0 {
			continue
		}
		node, err := r.nodeOfServer(ctx, snap, sid)
		if err != nil {
			return h, err
		}
		if node == "" {
			srv, err := r.hcache().server(ctx, sid)
			if err != nil && !errors.Is(err, hetzner.ErrNotFound) {
				return h, err
			}
			// A server named like the node in status is our former holder
			// whose Node object is gone; anything else is foreign.
			if srv == nil || group.Status.Node == "" || srv.Name != group.Status.Node {
				return holder{foreign: fmt.Sprintf(
					"Floating IP %s is assigned to server %d, which is not a node of this cluster", f.fip.Name, sid)}, nil
			}
			node = srv.Name
		}
		if h.node == "" || node == group.Status.Node {
			h.node, h.server = node, sid
		}
	}
	if h.node == "" {
		h.node = group.Status.Node
	}
	if h.node == "" {
		return h, nil
	}
	if n := snap.byName[h.node]; n == nil {
		h.nodeGone = true
	} else if h.server == 0 {
		sid, err := r.serverOf(ctx, n)
		if err != nil && !errors.Is(err, hetzner.ErrNotFound) {
			return h, err
		}
		h.server = sid
	}
	if h.server == 0 {
		h.serverGone = !h.nodeGone
		return h, nil
	}
	srv, err := r.hcache().server(ctx, h.server)
	switch {
	case errors.Is(err, hetzner.ErrNotFound):
		h.serverGone = true
	case err != nil:
		return h, err
	case srv.Status == serverStatusOff:
		h.serverGone = true
	}
	return h, nil
}

// assign puts every FIP on the server. The holder is re-read from Hetzner
// first; a change since the last poll aborts with errHolderChanged.
func (r *FloatingIPGroupReconciler) assign(ctx context.Context, fips []groupFIP, server int64) error {
	aligned := true
	for _, f := range fips {
		aligned = aligned && f.fip.ServerID == server
	}
	if aligned {
		return nil
	}
	for i := range fips {
		fresh, err := r.hcache().refreshFIP(ctx, fips[i].fip.ID)
		if err != nil {
			return err
		}
		if fresh.ServerID != fips[i].fip.ServerID {
			return errHolderChanged
		}
		fips[i].fip = fresh
	}
	// Issue every assignment before waiting, so both families switch together.
	var ids []int64
	for _, f := range fips {
		if f.fip.ServerID != server {
			ids = append(ids, f.fip.ID)
		}
	}
	done, err := r.Hetzner.AssignAll(ctx, server, ids)
	for i := range fips {
		if slices.Contains(done, fips[i].fip.ID) {
			cp := *fips[i].fip
			cp.ServerID = server
			r.hcache().putFIP(&cp)
			fips[i].fip = &cp
		}
	}
	if err != nil {
		return fmt.Errorf("assign floating IPs to server %d: %w", server, err)
	}
	return nil
}
