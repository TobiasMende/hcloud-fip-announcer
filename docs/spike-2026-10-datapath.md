# Spike 2026-10: datapath for foreign-class LoadBalancer IPs

Date: 2026-10-23. Cluster: Talos 1.14.1, Kubernetes 1.36.5, Cilium 1.20.3 with
kube-proxy replacement (tunnel mode, `devices: eth0,eth1,tailscale0`,
`nodePort.addresses: [10.0.0.0/8]`), Hetzner Cloud fsn1, nodes with primary
IPv4 and default route via eth0.

## Setup

- Floating IPv4 assigned to one worker.
- nginx (TCP) and socat UDP echo pinned to that worker.
- Two Services `type: LoadBalancer`, `loadBalancerClass: fip-announcer.inocea.de/floating-ip`,
  `externalTrafficPolicy: Local`; `status.loadBalancer.ingress` written by hand
  (`ip: <FIP>`, `ipMode: VIP`).
- Temporary Hetzner firewall opening the test ports for the tester's IP.

## Results

| Check | Result |
|---|---|
| TCP to FIP:18080 from the internet | 200 |
| UDP to FIP:9999 (echo) | echoed |
| Client IP seen by nginx | the tester's real public IP (eTP Local preserves it) |
| `cilium-dbg service list` | LoadBalancer frontends for the FIP (TCP and UDP) with the local backend |
| FIP moved to a worker without endpoints | no answer on TCP or UDP (expected with eTP Local) |
| FIP moved back via API | answering again 7 s after the assign call started |

## Decision

Cilium kube-proxy replacement serves LoadBalancer IPs written into the Service
status by a controller with a foreign `loadBalancerClass`. Datapath mode for
v0.1: **write `status.loadBalancer.ingress`** (no `externalIPs` fallback
needed). A FIP assignment takes about 6–7 s end to end, which leaves room for
the 10–15 s failover goal only if detection stays in the low single seconds.
