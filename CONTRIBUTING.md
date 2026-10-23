# Contributing

## Setup

You need Go (version in `go.mod`), Docker (for image builds) and, for chart
work, Helm with the `unittest` plugin. Tool binaries (controller-gen,
golangci-lint, setup-envtest, kustomize) are downloaded into `bin/` on first
use.

```sh
git clone https://github.com/TobiasMende/hcloud-fip-announcer
cd hcloud-fip-announcer
make test
```

## Make targets

| Target | What it does |
|---|---|
| `make build` | Generate, format, vet and build the manager binary |
| `make test` | Unit and envtest tests (downloads envtest assets on first run) |
| `make lint` / `make lint-fix` | golangci-lint |
| `make manifests` / `make generate` | Regenerate CRD, RBAC and deepcopy code after changing `api/` or kubebuilder markers |
| `make chart-crds` | Regenerate the CRD and copy it, the dashboard and the alert rules into the chart |
| `make chart-test` | Lint the chart and run helm-unittest |
| `make run` | Run the controller against your current kubeconfig |
| `make docker-build` | Build the image |

Generated files (CRDs, `zz_generated.deepcopy.go`, files under
`charts/hcloud-fip-announcer/crds` and `files`) are committed; run the targets
above and commit the result.

## Pull requests

- Keep changes focused and add tests for behaviour changes. Eligibility and
  placement are pure packages and easy to test; Hetzner calls go through the
  fake in `internal/hetzner`.
- Everything is English: code, comments, docs, commit messages.
- Update the docs in `docs/` when flags, values, metrics or behaviour change.
- Use [Conventional Commits](https://www.conventionalcommits.org/)
  (`feat:`, `fix:`, `docs:`, `chore:`, `feat!:` for breaking changes).
  release-please builds the changelog and versions from them.
- No DCO or CLA. By contributing you agree your work is licensed under
  Apache-2.0.
