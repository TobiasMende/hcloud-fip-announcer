# How it works

## LoadBalancer = IPAM + announcement

A Service of `type: LoadBalancer` needs two things:

1. **IPAM**: an address in `status.loadBalancer.ingress`.
2. **Announcement**: a way for traffic to that address to reach a node with a
   ready endpoint. MetalLB or Cilium do this with ARP or BGP.

Hetzner Cloud has neither. A Floating IP is routed to exactly one server, and
the Hetzner API can move it. So the announcement is an API call.
hcloud-fip-announcer does both halves for Services of its class:

- It writes the group's Floating IP into `status.loadBalancer.ingress`
  (`ipMode: VIP`). Datapaths serving LoadBalancer IPs from the Service status
  (Cilium kube-proxy replacement, kube-proxy) deliver traffic that arrives on
  whichever node holds the IP.
- It keeps the Floating IP assigned to a node where every Service of the group
  has a ready endpoint.

Services of a foreign `loadBalancerClass` are ignored by the Hetzner cloud
controller manager and by Cilium LB-IPAM, so nobody fights over them.

## Groups

A `FloatingIPGroup` is one or two Floating IPs (IPv4 and/or IPv6) that always
move together, plus an optional `nodeSelector`. Several groups are independent.
Services join a group with the annotation `fip-announcer.inocea.de/group`;
several Services (TCP and UDP, different ports) may share one group.

The controller adopts a Floating IP by name or creates it (home location from
`spec.location`). It looks the name up in Hetzner before creating, even when
its cached list does not have it yet. It labels the IPs it manages with
`fip-announcer.inocea.de/cluster-id` and `fip-announcer.inocea.de/group` and
keeps delete protection on for every one of them, adopted or created.

## Eligible nodes

A node can hold a group's IPs when all of this holds:

1. The Node is `Ready`, not cordoned, not being deleted.
2. It matches `spec.nodeSelector`, if set.
3. It is not a control-plane node (`node-role.kubernetes.io/control-plane`),
   unless the `nodeSelector` selects it.
4. Every member Service has a ready EndpointSlice endpoint on it.
5. Every member Service's health check passes on it (see below).
6. It maps to a Hetzner server (`spec.providerID` `hcloud://<id>`, else a
   server named like the node) in the network zone of the Floating IP.

A group without member Services is placed only if it has a `nodeSelector`;
otherwise it reports `NoMembers` and the IPs are not placed.

## Placement

- **Stick**: if the holder is eligible, nothing happens. There is no failback
  and no rebalancing.
- **Move** when the holder stops being eligible. The target is the eligible
  node in the IPs' home location (Hetzner routes a Floating IP across its
  network zone, but another location costs an extra hop), then the node
  holding the fewest IPs of other groups, then (avoiding the node the leader
  pod runs on) the first by name. All IPs of the group are assigned to it
  together.
- **No eligible node**: the IPs stay where they are, the group is `Degraded`
  (`NoEligibleNode`), a warning event is emitted. They are never assigned to
  an ineligible node.
- **Anti-flap**: at least `--anti-flap` (30 s) between two moves of a group.
  Exceptions that move at once: the holder is cordoned, or its server is gone
  or powered off.

## Probing

Kubernetes marks endpoints not ready only after about 40-50 s. To be faster,
the leader probes the **pod IPs of the ready endpoints** on each candidate node
for every member Service, every `--probe-interval` (1 s). After
`--probe-failure-threshold` (3) consecutive failures the target counts as
failing and the node becomes ineligible. One success makes it healthy again.

The check comes from the Service annotation `fip-announcer.inocea.de/health-check`
(`http://:<port><path>`, 2xx expected, or `tcp://:<port>`). Without the
annotation the controller does a TCP connect to the first TCP port of the
endpoints; a UDP-only Service without annotation is decided by readiness
alone. An invalid annotation emits an `InvalidHealthCheck` event and uses
that default. Probes never call the Hetzner API. In a default-deny namespace the
controller needs egress to those pods (see the chart's `NOTES.txt` for a
NetworkPolicy).

A Hetzner assignment takes about 6-7 s end to end (measured in the
[spike](spike-2026-10-datapath.md)), so a failover is detection time plus that.

## Hetzner API budget

Hetzner allows 3600 requests/h per project. The controller lists all Floating
IPs and all servers once per `--hetzner-poll-interval` (30 s, minimum 10 s):
two list calls per interval (more if the lists exceed one page), about 240
requests/h at the default. Before each move it re-reads the affected Floating
IPs; if the holder changed since the last poll it decides again on fresh data.
429 and 5xx responses are retried with backoff. If a refresh fails the last
snapshot is kept and retried after a short backoff.

## Interrupted moves

Before the first Hetzner assign of a move the controller writes
`status.pendingMove` (target node, reason, start time). If that write fails,
nothing is assigned. Once the move is recorded in `status.node`, `reason` and
`since`, the pending move is cleared in the same write, and only then is the
move counted in `fip_announcer_moves_total` and reported as an event.

If the leader dies, or a write fails, between the assign and that record, any
leader that finds the Floating IPs on the pending target completes the move
with its real reason and its original start time as `since`; it is not
reported as `External`. Split Floating IPs (one family moved, the other not)
are aligned to the pending target if it is eligible, else to `status.node`.
Floating IPs found on a third node mean someone else moved them: `External`,
and the pending move is dropped. A pending move older than 10 minutes no
longer explains anything and is ignored. Anti-flap counts from the pending
move's start once its target holds the Floating IPs.

## Assignments made outside the controller

The holder is whatever Hetzner says.

- A Floating IP on another node of this cluster (moved in the console):
  accepted if that node is eligible (recorded with reason `External`), else
  moved to an eligible node.
- A Floating IP assigned to a server that is not a node of this cluster: never
  taken. The group is `Degraded` with reason `External` until a human resolves it.
- A Floating IP labelled for another cluster or group, or of the wrong IP
  family: the group is `Degraded` with reason `Refused`.

## Several clusters per Hetzner project

Every cluster needs its own `clusterId`. A Floating IP labelled for another
cluster is never touched. An unlabelled Floating IP adopted by name is
labelled. With `deletionPolicy: Delete` only IPs labelled for this cluster and
group are deleted.

## Leader election

Replicas elect a leader through the Lease `hcloud-fip-announcer` in the
release namespace (6 s lease, 4 s renew, 1 s retry by default). A leader that
shuts down releases the Lease, so a rolling update hands over at once. Only
the leader reconciles and probes. The pods avoid sharing a node (soft
anti-affinity), and the leader's node loses ties in placement, so losing the
node holding the IPs does not usually cost a leader election as well. To rule
it out, run the controller on control-plane nodes, which never hold a Floating
IP unless a group selects them (see
[Running on control-plane nodes](configuration.md#running-on-control-plane-nodes-recommended)).

## Known limitations

- IPv6 via Cilium is untested; NAT46/64 is planned.
- No `hostNetwork` mode yet; a per-node agent is planned.
- A leader that dies between recording a move and reporting it loses that
  move's event and counter increment (never double-counts it).
