# Configuration

## FloatingIPGroup

Cluster-scoped, `fip-announcer.inocea.de/v1alpha1`, short columns IPv4, Node,
Reason, Age.

| Field | Type | Default | Description |
|---|---|---|---|
| `spec.location` | string, required | | Hetzner home location (e.g. `fsn1`) for Floating IPs the controller creates. Adopted IPs keep theirs. |
| `spec.ipv4.name` | string, required if `ipv4` set | | Hetzner Floating IP name; adopted if it exists, created otherwise. |
| `spec.ipv6.name` | string, required if `ipv6` set | | Same for IPv6. |
| `spec.ipv6.ipv6Address` | string | `::1` | Address taken from the /64 and written to Service status; must be a valid IPv6 address. (Also accepted under `ipv4`, where it is unused.) |
| `spec.deletionPolicy` | `Retain` or `Delete` | `Retain` | `Retain` leaves the Floating IPs in Hetzner when the group is deleted. `Delete` disables delete protection, unassigns and deletes them, but only IPs labelled for this cluster and group. |
| `spec.nodeSelector` | label selector | none | Restricts eligible nodes; AND-combined with the member Services. A selector that matches control-plane nodes admits them. |

Validation:

- At least one of `ipv4` and `ipv6` must be set.
- The group name must be at most 63 characters (it becomes a Hetzner label value).

Status:

| Field | Description |
|---|---|
| `status.ipv4`, `status.ipv6` | Addresses of the group (for IPv6, the configured address inside the /64). |
| `status.node` | Node currently holding the Floating IPs. |
| `status.since` | When they arrived on that node. |
| `status.reason` | Why: `Initial`, `NodeCordoned`, `ProbeFailed`, `EndpointsNotReady`, `NodeNotReady`, `ServerGone`, `External`. |
| `status.conditions` | `Ready` and `Degraded`, see below. |

### Conditions

`Ready` is true when the IPs sit on an eligible node. `Degraded` is true when
the controller cannot do its job. The condition `reason` is one of:

| Reason | Ready | Degraded | Meaning |
|---|---|---|---|
| `Assigned` | true | false | IPs are on an eligible node. |
| `HolderIneligible` | false | false | The holder is ineligible; waiting for the anti-flap interval. |
| `NoMembers` | false | false | No member Services and no `nodeSelector`; not placed. |
| `NoEligibleNode` | false | true | No node is eligible; IPs stay where they are. |
| `Refused` | false | true | A named Floating IP has the wrong family or belongs to another cluster or group. |
| `External` | false | true | A Floating IP is assigned to a server that is not a node of this cluster. |

## Services

A Service is a member when it is `type: LoadBalancer`, matches the controller's
`loadBalancerClass`, and carries the group annotation.

| Annotation | Description |
|---|---|
| `fip-announcer.inocea.de/group` | Name of the `FloatingIPGroup`. Required. When it is removed, or the group does not exist or is being deleted, the controller clears the addresses it wrote (see below). |
| `fip-announcer.inocea.de/health-check` | `http://:<port><path>` (HTTP GET, 2xx expected) or `tcp://:<port>`. The host must be empty; probes go to the pod IPs of the ready endpoints on each node. Absent: TCP connect to the first TCP endpoint port; UDP-only Services are decided by endpoint readiness. An invalid value emits an `InvalidHealthCheck` event and falls back to the default check (as if absent). |

`spec.loadBalancerClass` must equal `--load-balancer-class`
(default `fip-announcer.inocea.de/floating-ip`). It is immutable on existing
Services.

**Takeover mode**: with `--load-balancer-class=""` the controller handles
annotated LoadBalancer Services **without** a class, like a default
implementation, so an existing Service can be taken over in place (its address
changes at once). Only safe when no other default implementation runs (Hetzner
cloud controller manager with its load balancer controller, Cilium LB-IPAM
with a default pool, MetalLB). The controller emits a `ForeignStatus` warning
when a Service already carries an address it did not write.

The controller writes one `status.loadBalancer.ingress` entry per entry of the
Service's `ipFamilies` (IPv4 if unset). A family the group has no address for
emits a `NoAddress` warning.

Together with the status, the controller records the addresses it wrote in the
annotation `fip-announcer.inocea.de/published` (comma-separated; managed by the
controller, do not edit). It decides what to release from it:

- When the group annotation is removed, or the group does not exist or is being
  deleted, the controller clears the ingress of Services of its class; in
  takeover mode it removes only the addresses listed as published and keeps
  anything another implementation wrote.
- A group with `deletionPolicy: Delete` releases its member Services before it
  deletes the Floating IPs.
- `ForeignStatus` fires when the ingress contains an address that is not listed
  as published.

Use `externalTrafficPolicy: Local` to preserve client IPs; the group only
moves to nodes with a ready local endpoint.

## Controller flags

| Flag | Default | Description |
|---|---|---|
| `--cluster-id` | required | Identifies this cluster in the labels of its Floating IPs. |
| `--load-balancer-class` | `fip-announcer.inocea.de/floating-ip` | Class of the Services to handle; empty handles Services without a class. |
| `--anti-flap` | `30s` | Minimum time between two moves of a group (not applied when the holder is cordoned or its server is gone). |
| `--hetzner-poll-interval` | `30s` | How long Hetzner reads are cached; minimum `10s`. |
| `--max-concurrent-reconciles` | `4` | Groups reconciled in parallel. |
| `--probe-interval` | `1s` | Interval between probes; also the per-probe timeout. |
| `--probe-failure-threshold` | `3` | Consecutive failures before a target counts as failing. |
| `--leader-elect` | `false` | Enable leader election (the chart always sets it). |
| `--leader-elect-lease-duration` | `6s` | How long non-leaders wait before taking over. |
| `--leader-elect-renew-deadline` | `4s` | How long the leader retries renewing. |
| `--leader-elect-retry-period` | `1s` | Time between election attempts. |
| `--metrics-bind-address` | `0` (off) | Metrics address, e.g. `:8080`. |
| `--metrics-secure` | `true` | Serve metrics over HTTPS with authn/authz. |
| `--metrics-cert-path`, `--metrics-cert-name`, `--metrics-cert-key` | `""`, `tls.crt`, `tls.key` | Metrics server certificate. |
| `--health-probe-bind-address` | `:8081` | Serves `/healthz` and `/readyz`. |
| `--enable-http2` | `false` | Enable HTTP/2 on the metrics and webhook servers. |
| `--webhook-port`, `--webhook-cert-path`, `--webhook-cert-name`, `--webhook-cert-key` | `9443`, `""`, `tls.crt`, `tls.key` | Webhook server options (no webhooks are used at present). |
| zap flags (`--zap-log-level`, `--zap-devel`, ...) | | Logging. |

Environment: `HCLOUD_TOKEN` (required), `NODE_NAME` (the pod's node; the chart
sets it; used to prefer other nodes than the leader's on placement ties).

## Helm values

Chart: `oci://ghcr.io/tobiasmende/charts/hcloud-fip-announcer`.

| Key | Default | Description |
|---|---|---|
| `clusterId` | `""` (required) | Passed as `--cluster-id`. |
| `image.repository` | `ghcr.io/tobiasmende/hcloud-fip-announcer` | Image. |
| `image.tag` | `""` | Defaults to the chart `appVersion`. |
| `image.pullPolicy` | `IfNotPresent` | |
| `imagePullSecrets` | `[]` | |
| `nameOverride`, `fullnameOverride` | `""` | |
| `replicaCount` | `2` | One leader, the rest standby. |
| `hcloud.existingSecret` | `""` | Existing Secret holding the token. |
| `hcloud.existingSecretKey` | `token` | Key in that Secret. |
| `hcloud.token` | `""` | Create a Secret from this value (project-wide read/write token). One of the two is required. |
| `controller.loadBalancerClass` | `fip-announcer.inocea.de/floating-ip` | `""` selects Services without a class. |
| `controller.antiFlap` | `30s` | |
| `controller.hetznerPollInterval` | `30s` | |
| `controller.maxConcurrentReconciles` | `4` | |
| `controller.probeInterval` | `1s` | |
| `controller.probeFailureThreshold` | `3` | |
| `controller.leaderElection.leaseDuration` | `6s` | |
| `controller.leaderElection.renewDeadline` | `4s` | |
| `controller.leaderElection.retryPeriod` | `1s` | |
| `controller.extraArgs` | `[]` | Extra container args. |
| `metrics.port` | `8080` | |
| `metrics.secure` | `false` | Plain HTTP so Prometheus can scrape without token auth. |
| `healthProbePort` | `8081` | |
| `serviceAccount.create`, `.name`, `.annotations` | `true`, `""`, `{}` | |
| `podAnnotations`, `podLabels` | `{}` | |
| `priorityClassName` | `""` | Consider `system-cluster-critical`. |
| `resources` | requests 20m CPU / 64Mi, limit 128Mi memory | |
| `nodeSelector`, `tolerations` | `{}`, `[]` | |
| `affinity` | `{}` | Replaces the default soft anti-affinity on `kubernetes.io/hostname`. |
| `podDisruptionBudget.enabled`, `.minAvailable` | `true`, `1` | |
| `serviceMonitor.enabled`, `.interval`, `.labels` | `false`, `30s`, `{}` | Prometheus Operator `ServiceMonitor`. |
| `prometheusRule.enabled`, `.labels` | `false`, `{}` | Alert rules (also in [examples/alerts.rules.yaml](../examples/alerts.rules.yaml)). |
| `dashboard.enabled` | `false` | ConfigMap with the Grafana dashboard for the sidecar. |
| `dashboard.label`, `dashboard.labelValue` | `grafana_dashboard`, `"1"` | Sidecar label. |
| `dashboard.annotations` | `{}` | |
| `dashboard.grafanaOperator.enabled` | `false` | Create a `GrafanaDashboard` for grafana-operator v5. |
| `dashboard.grafanaOperator.instanceSelector` | `matchLabels: {dashboards: grafana}` | |

## Metrics

| Metric | Labels | Description |
|---|---|---|
| `fip_announcer_assignment` | `group`, `ip`, `node` | 1 for the node currently holding each address. |
| `fip_announcer_moves_total` | `group`, `reason` | Moves by reason (including `External`). Every reason starts at 0 when a group is first reconciled. |
| `fip_announcer_last_move_timestamp_seconds` | `group` | Unix time the IPs arrived on the current node. |
| `fip_announcer_eligible_nodes` | `group` | Number of eligible nodes. |
| `fip_announcer_group_degraded` | `group` | 1 when `Degraded`. |
| `fip_announcer_probe_healthy` | `group`, `node`, `service` | 1 healthy, 0 failing. |
| `fip_announcer_hcloud_requests_total` | `method`, `code` | Hetzner API requests (every attempt, retries included). |
| `fip_announcer_hcloud_request_duration_seconds` | | Hetzner API latency histogram. |

Leadership is visible as `leader_election_master_status` (controller-runtime);
the usual `controller_runtime_*` and `workqueue_*` metrics are served too.
Alert rules: `GroupDegraded`, `NoEligibleNode`, `HcloudAPIErrors`, `NoLeader`,
`FrequentMoves`.

## Events

| Reason | Type | On | Meaning |
|---|---|---|---|
| `Moved` | Normal | group, old and new Node, member Services | Floating IPs moved (or were found on another node). |
| `Created`, `Adopted` | Normal | group | Floating IP created in Hetzner, or adopted and labelled. |
| `Deleted` | Normal | group | Deleted with `deletionPolicy: Delete`. |
| `NotDeleted` | Warning | group | IP not labelled for this group; left alone. |
| `NoEligibleNode`, `Refused`, `External` | Warning | group | The group became `Degraded` for that reason. |
| `UnknownGroup` | Warning | Service | The annotated group does not exist; the addresses the controller wrote are cleared. |
| `NoAddress` | Warning | Service | The group has no address for one of the Service's IP families. |
| `ForeignStatus` | Warning | Service | Takeover mode: the Service carries an address not listed in `fip-announcer.inocea.de/published`. |
| `InvalidHealthCheck` | Warning | Service | Unparseable health-check annotation; the default check is used instead. |
