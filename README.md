# hcloud-fip-announcer

Kubernetes `type: LoadBalancer` needs an address and a way for traffic to that
address to reach a node that can serve it. Hetzner Cloud has no ARP or BGP, so
the only lever is its API: a Floating IP is routed to exactly one server and
can be moved. `hcloud-fip-announcer` keeps Floating IPs on a node that can
serve the Services using them — moved before planned maintenance (cordon),
moved fast when a node fails (active probing). TCP and UDP, client IPs
preserved with `externalTrafficPolicy: Local`.

API `v1alpha1`; breaking changes are called out in the release notes.

## Requirements

- Hetzner Cloud, and an API token for the project (see [security](docs/security.md)).
- Nodes are Hetzner Cloud servers with a public IPv4 primary IP on `eth0`,
  located in the network zone of the Floating IP.
- A datapath that serves the IPs in `status.loadBalancer.ingress`. Tested with
  Cilium kube-proxy replacement with `eth0` in its `devices`. kube-proxy
  should work but is untested.
- Kubernetes with `EndpointSlice` and `status.loadBalancer.ingress[].ipMode`
  (1.30+ for the latter).

## Quick start

```sh
kubectl create namespace fip-announcer
kubectl -n fip-announcer create secret generic hcloud --from-literal=token=<hetzner-api-token>

helm install hcloud-fip-announcer \
  oci://ghcr.io/tobiasmende/charts/hcloud-fip-announcer \
  --namespace fip-announcer \
  --set clusterId=my-cluster \
  --set hcloud.existingSecret=hcloud
```

(Or `--set hcloud.token=...` to let the chart create the Secret.)

One group, adopting the Floating IP `web-v4` or creating it:

```yaml
apiVersion: fip-announcer.inocea.de/v1alpha1
kind: FloatingIPGroup
metadata:
  name: web
spec:
  location: fsn1
  ipv4:
    name: web-v4
```

One Service using it:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: web
  annotations:
    fip-announcer.inocea.de/group: web
    fip-announcer.inocea.de/health-check: http://:8080/healthz
spec:
  type: LoadBalancer
  loadBalancerClass: fip-announcer.inocea.de/floating-ip
  externalTrafficPolicy: Local
  selector:
    app: web
  ports:
    - {name: http, port: 80, targetPort: 8080, protocol: TCP}
```

```sh
kubectl get floatingipgroup web      # IPv4, Node, Reason
kubectl get svc web                  # EXTERNAL-IP is the Floating IP
```

More in [examples/](examples/).

## Documentation

- [How it works](docs/how-it-works.md) — IPAM vs announcement, placement, probing, limits
- [Configuration](docs/configuration.md) — CRD, annotations, flags, Helm values, metrics, events
- [Maintenance](docs/maintenance.md) — cordon, reboot, upgrade the controller
- [Security](docs/security.md) — token scope, RBAC, verifying images
- [Design](docs/design.md) — original design document
- [Datapath spike](docs/spike-2026-10-datapath.md) — Cilium test results

## Known limitations

- IPv6 with Cilium is untested; NAT46/64 support is planned.
- No `hostNetwork` mode yet (a Floating IP as an address on the node
  interface, needed for e.g. TURN relay ports); a per-node agent is planned.
- A leader change in the middle of a move can briefly re-align split Floating
  IPs of a group before the move completes.

## Development

```sh
make build   # build the manager binary
make lint    # golangci-lint
make test    # unit and envtest tests (downloads envtest assets on first run)
```

See [CONTRIBUTING.md](CONTRIBUTING.md). License: Apache-2.0.
