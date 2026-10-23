# hcloud-fip-announcer — design

Status: draft · 2026-10-23 · Owner: Tobias Mende (GitHub `TobiasMende`)

## Intent

Kubernetes `type: LoadBalancer` needs two things the cluster does not provide
by itself: an **IP address** in `status.loadBalancer.ingress` (IPAM) and a way
for traffic to that address to **reach a node with a ready endpoint**
(announcement). On bare metal, MetalLB or Cilium do the announcement with ARP
or BGP. Hetzner Cloud has neither; the only lever is its API: a Floating IP
(FIP) is routed to exactly one server, and the API can move it.

`hcloud-fip-announcer` is that missing announcer. It keeps Hetzner Floating
IPs on a node that can actually serve the Services using them, moves them
before planned maintenance, and moves them fast when a node fails. Services
opt in with a dedicated `loadBalancerClass`; for those Services the controller
also writes the FIP into `status.loadBalancer.ingress`, so neither the Hetzner
cloud controller manager nor another IPAM touches them.

Why now: clusters on Hetzner Cloud that want to replace the Hetzner Load
Balancer — TCP only, an extra hop, ~7 €/month — with a Floating IP that also
carries UDP (HTTP/3 now, TURN/VoIP later). The first users run Talos, Cilium
with kube-proxy replacement and an ingress controller as a DaemonSet; the
project must not depend on any of these. No maintained project does this
well: `costela/hcloud-ip-floater` is the right
model but unmaintained since 2024; `cbeneke/hcloud-fip-controller` is
maintained but leader-centric and does not consider endpoint readiness or
node health on its pod path.

## Goals

1. **Minimal downtime.** Planned maintenance: the FIP moves *before* the node
   goes down (trigger: the node is cordoned). Unplanned failure: move as fast
   as possible — target ≤ 10–15 s from failure to the FIP answering on the new
   node, including the Hetzner API call.
2. **Service-aware.** A FIP only goes to a node where every Service using it
   has a ready endpoint (works with `externalTrafficPolicy: Local`, so client
   IPs are preserved). TCP and UDP Services alike.
3. **Several independent groups.** One controller manages several FIP groups;
   each group moves on its own (e.g. one follows Traefik, one a TURN server).
4. **Manages the FIPs end to end.** Finds a FIP by name or creates it, never
   loses it by accident.
5. **Observable.** Metrics, structured logs, Kubernetes events, a status field,
   a Grafana dashboard and alert rules others can adopt.
6. **A proper open-source project.** Public on GitHub under `TobiasMende`,
   Apache-2.0, everything in English (code, comments, README, docs, commits),
   tests, CI, Renovate, signed multi-arch images on GHCR, Helm chart.

## Non-goals (v1)

- `hostNetwork` workloads (FIP as an address on the node interface, needed
  for TURN relay port ranges). The design must leave room for it: a later
  per-node agent that adds/removes the address.
- General IPAM. The controller only binds Services of its class to its
  groups' FIP addresses; it does not hand out addresses from pools.
- Load balancing across nodes, failback to a "home" node, rebalancing.
- Hetzner dedicated servers / vSwitch, other clouds.
- Egress through the FIP (a fixed source IP). The later per-node agent should
  be designed so it could add this.
- A manual "move now" field. Cordoning the holder is the manual lever.

## Anti-goals (failure even if it "works")

- A FIP released or recreated without an explicit `deletionPolicy: Delete`
  (new IP = DNS change, broken third-party allowlists).
- A FIP placed on a node without ready endpoints for all its Services to
  "force" progress.
- Flapping between nodes.
- Needing Talos or Cilium for the core function.

## Concepts

### FloatingIPGroup (CRD)

Cluster-scoped custom resource (API group: see "Left to the builder"). One group = up to two FIPs (one IPv4, one IPv6;
at least one) that always move together, plus the rules for choosing a node.

```yaml
apiVersion: <group>/v1alpha1
kind: FloatingIPGroup
metadata:
  name: ingress
spec:
  location: fsn1                  # Hetzner home location for created FIPs
  ipv4:
    name: ingress-v4              # Hetzner FIP name: adopt if it exists, else create
  ipv6:                           # optional; omit for IPv4-only
    name: ingress-v6
  deletionPolicy: Retain          # Retain (default) | Delete
  nodeSelector:                   # optional, AND-combined with services
    matchLabels: { traefik: enabled }
status:
  ipv4: 203.0.113.10
  ipv6: 2001:db8:1:2::1          # the address taken from the /64 (see below)
  node: worker-node-2
  since: "2026-10-24T08:00:00Z"
  reason: NodeCordoned            # Initial | NodeCordoned | ProbeFailed | EndpointsNotReady | NodeNotReady | Manual
  conditions: [Ready, Degraded, …] # Degraded: no eligible node, FIP stays put
```

### Service membership (loadBalancerClass)

- A Service opts in with `spec.loadBalancerClass: <api-group>/floating-ip`
  (exact string follows the API group, see "Left to the builder") and an
  annotation naming its group (`…/group: ingress`).
- For these Services the controller writes the group's addresses (IPv4 and/or
  IPv6, per the Service's `ipFamilies`) into `status.loadBalancer.ingress`.
  Hetzner's cloud controller manager and Cilium LB-IPAM ignore Services with a
  foreign class, so nobody fights over them. Datapaths that serve LoadBalancer
  IPs from the Service status (Cilium kube-proxy replacement, kube-proxy) then
  deliver the traffic on whichever node receives it.
- Several Services may share one group (TCP and UDP, different ports).
- `loadBalancerClass` is immutable on existing Services: adopting an
  existing workload means creating a new Service of the class and deleting the
  old one after cutover.

### Health checks

Per-Service, via annotation (prefix decided by the builder):

- `…/health-check: http://:8080/ping` — HTTP GET, expect 2xx.
- `…/health-check: tcp://:3478` — TCP connect.
- absent — EndpointSlice readiness only.

Probes target the **pod IPs of the ready endpoints on that node** (port from
the check). No chart changes are needed in the workload, and a dead node fails
the probe at once because its pods stop answering. UDP itself is not probed
generically; a UDP Service names a TCP/HTTP check of its pods or relies on
readiness. The Helm chart documents the network policy the controller needs to
reach those pods.

### Eligible node

A node is eligible for a group when all of these hold:

1. Node `Ready`, not cordoned (`spec.unschedulable` false), not being deleted.
2. Matches `nodeSelector` if set.
3. Every member Service has at least one EndpointSlice endpoint on that node
   with `conditions.ready: true` (`nodeName` on the endpoint).
4. Every member Service's health check (if any) passes on that node.
5. The node maps to a Hetzner server (by `spec.providerID` `hcloud://<id>`,
   fallback: node name = server name) in a location within the FIP's network
   zone.
6. Not a control-plane node (label `node-role.kubernetes.io/control-plane`),
   unless the group's `nodeSelector` explicitly selects it.

A group without member Services: with a `nodeSelector`, any matching
eligible node; without one, the FIP stays unassigned until a member exists.

## Behaviour

### Placement

- **Stick:** if the current holder is eligible, do nothing. No failback, no
  rebalancing.
- **Move** when the holder stops being eligible: pick an eligible node (prefer
  the one with the fewest group FIPs, then a stable order such as name) and
  assign every FIP of the group to it.
- **No eligible node:** leave the FIP where it is, set `Degraded`, emit an
  event and a metric. Never assign to an ineligible node.
- **Anti-flap:** a minimum interval between moves of one group (default e.g.
  30 s) except when the holder is cordoned or its server is gone.

### Planned maintenance

Cordon is the signal. When the holder is cordoned, the group moves at once
(skipping the anti-flap delay) to another eligible node. Tooling waits for
`status.node` to change (`kubectl wait --for=jsonpath=…`), then reboots or
deletes the node, then uncordons. Upgrade scripts (e.g. a Talos upgrade loop)
get that step.

### Unplanned failure — fast detection

Kubernetes only marks endpoints not-ready after ~40–50 s (node monitor grace
period). To reach the goal, the controller probes actively:

- The leader probes every candidate node for each group (holder and
  alternatives) at a short interval (default 1 s, configurable) using the
  Service health checks, or a TCP connect to the Service's node port/target
  when no check is configured but a TCP port exists.
- N consecutive failures (default 3) on the holder → move.
- Probes run from the leader pod; the leader should therefore not run on the
  node that holds the FIP (soft anti-affinity / leadership hand-off), so that
  losing that node does not also cost a leader election.
- Leader election via a Kubernetes Lease with short timings (defaults around
  lease 6 s / renew 4 s / retry 1 s; configurable).

### Hetzner API

- Assign via `FloatingIP.Assign`; treat "already assigned to that server" as
  success; retry with backoff; respect rate limits (Hetzner: 3600 requests/h
  per project) — probing must never hit the Hetzner API.
- IPv6 FIPs are /64 networks: the group uses one address from the /64
  (default `::1`, configurable) as the Service status address.
- Adopt by name; create with `home_location`, the group's labels and delete
  protection enabled. `Delete` policy: disable protection, unassign, delete.

### Assignments made outside the controller

The real holder is whatever the Hetzner API says (polled about every 30 s,
well inside the 3600 requests/h project limit, and immediately before each
move).

- FIP on another cluster node (someone moved it in the console): accepted if
  that node is eligible, otherwise moved to an eligible node.
- FIP on a server outside the cluster (found while adopting by name): never
  taken; the group is `Degraded` until a human resolves it.

### Several clusters per Hetzner project

The controller labels the FIPs it creates or adopts with its cluster ID and
group (`cluster-id` is a required Helm value) and never touches a FIP labelled
for another cluster or group. Adopting an unlabelled FIP by name labels it.

## Observability

- **Metrics** (Prometheus): current holder per group/address
  (`…_assignment{group,ip,node} 1`), moves total by reason, time since last
  move, probe results and latency per group/node, eligible node count,
  Hetzner API calls/errors/latency, leader status, reconcile errors.
- **Logs:** structured JSON; every move one line with group, IPs, from, to,
  reason.
- **Events** on the FloatingIPGroup, the Node and the member Services.
- **Grafana dashboard** in the repo (raw JSON, ConfigMap with the
  `grafana_dashboard: "1"` sidecar label, `GrafanaDashboard` for the Grafana
  Operator), switchable in the Helm chart.
- **Alert rules** (PrometheusRule + plain rules file): group degraded, no
  eligible node, Hetzner API errors, no leader, move rate high.

## Security

- The Hetzner token is project-wide read/write (Hetzner cannot scope it).
  It lives in one Secret readable only by the controller's ServiceAccount;
  the docs state plainly what the token can do.
- Non-root, read-only root filesystem, no privileges, minimal RBAC: read
  Services, EndpointSlices, Nodes; write Events and Leases; own CRD and its
  status; write `status` of Services of its class.
- Signed images (cosign), SBOM, pinned dependencies, Renovate.

## Project and delivery

- Repo `github.com/TobiasMende/hcloud-fip-announcer`, public, Apache-2.0,
  English throughout.
- Go, controller-runtime / kubebuilder layout, `hcloud-go/v2`.
- Tests: unit tests for eligibility, placement and anti-flap; controller tests
  with envtest; a fake Hetzner API for assignment logic. End-to-end against a
  real Hetzner project is manual/optional (costs money).
- CI (GitHub Actions): lint, test, build; on tag: multi-arch images
  (amd64/arm64) to `ghcr.io/tobiasmende/hcloud-fip-announcer`, cosign
  signature, SBOM, Helm chart as OCI artifact on GHCR, GitHub release notes.
- Renovate for Go modules, Actions and base images.
- Docs: README (what/why, quick start), configuration reference, the
  LoadBalancer/IPAM/announcer explanation, examples (Cilium kube-proxy
  replacement, kube-proxy), Grafana/alerts,
  maintenance runbook (cordon → wait → reboot).

## Assumptions to verify first (spike)

1. The datapath delivers traffic for a LoadBalancer IP that a *foreign*
   `loadBalancerClass` controller wrote into the Service status. Expected for
   kube-proxy; must be confirmed for Cilium kube-proxy replacement (including
   with `externalTrafficPolicy: Local` and UDP). If Cilium skips foreign-class
   Services, fall back to `spec.externalIPs` written by the controller or to
   Cilium LB-IPAM with a `serviceSelector`.
2. Ingress controllers that publish Gateway/Ingress status from a Service
   (Traefik `statusAddress.service`, ingress-nginx `publish-service`) can point
   at a Service of this class, so DNS automation follows.
3. A Floating IP routed to a server with its own primary IPv4 arrives on the
   public interface and replies leave via that interface's default route.

## Left to the builder

API group, `loadBalancerClass` string and annotation prefix (one consistent
domain), exact default timings within the stated goal, CRD field names,
metric names, Helm values layout, how the leader avoids the holder node, the
probing implementation (goroutine per node/group, connection reuse).

## Decisions made while writing (check these)

- Health checks probe pod IPs of local ready endpoints, not node ports.
- Foreign assignment: accept on an eligible cluster node, never take from a
  non-cluster server.
- Ownership labels on FIPs; `cluster-id` is a required Helm value.
- Control planes excluded unless selected; groups without members only place
  with a `nodeSelector`.
- No manual move field in v1; cordon is the lever.
