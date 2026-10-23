# hcloud-fip-announcer

Keeps Hetzner Cloud Floating IPs on a Kubernetes node that can serve the
LoadBalancer Services using them — moved before planned maintenance, moved
fast when a node fails. TCP and UDP.

Status: design phase. See [docs/design.md](docs/design.md).

## Development

```sh
make build   # build the manager binary
make lint    # golangci-lint
make test    # unit and envtest tests (downloads envtest assets on first run)
```
