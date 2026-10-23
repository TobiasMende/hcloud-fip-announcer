# Security

## The Hetzner token

Hetzner Cloud API tokens are scoped to a **project** and are either read-only
or read/write; nothing finer exists. The controller needs read/write. A leaked
token can therefore, for the whole project:

- read, create, modify and delete servers, volumes, networks, load balancers,
  firewalls, SSH keys, Floating IPs, certificates and other resources,
- power off or delete servers and delete volumes,
- read the project's metadata and labels.

The controller itself only lists Floating IPs and servers, creates, labels,
assigns, protects and deletes Floating IPs, and reads single servers.
Mitigation is isolation: use a **dedicated Hetzner project** for the cluster,
keep the token out of Git, and rotate it when in doubt (create a new token,
update the Secret, restart the Deployment, delete the old token).

## Secret handling

- Prefer `hcloud.existingSecret` over `hcloud.token`. A token passed as a Helm
  value ends up in the Helm release Secret and in your values files.
- Create the Secret with SOPS, External Secrets, Sealed Secrets or similar, in
  the controller's namespace. The token is exposed to the container as the
  environment variable `HCLOUD_TOKEN`.
- Restrict who can read Secrets in that namespace.

## Runtime hardening

The pod runs as non-root with a read-only root filesystem, no privilege
escalation, all capabilities dropped and the `RuntimeDefault` seccomp profile.
The image is distroless.

## RBAC

Cluster-wide:

| Resources | Verbs |
|---|---|
| `services` | get, list, watch, patch |
| `nodes` | get, list, watch |
| `endpointslices` (discovery.k8s.io) | get, list, watch |
| `services/status` | get, patch |
| `events` | create, patch |
| `floatingipgroups` (fip-announcer.inocea.de) | get, list, watch, update, patch |
| `floatingipgroups/status` | get, update, patch |
| `floatingipgroups/finalizers` | update |

In the release namespace: `leases` (coordination.k8s.io) get, list, watch,
create, update, patch for leader election.

The controller can patch the status of every Service of its class (and, in
takeover mode, of every annotated Service without a class). `patch` on
`services` exists for one annotation, `fip-announcer.inocea.de/published`,
which records the addresses it wrote; RBAC cannot narrow a patch to metadata,
so the permission technically covers Service specs too. The controller never
changes a spec.

## Network policy

If the namespace is default-deny, the controller needs egress to DNS, the
Hetzner API (`api.hetzner.cloud`, 443), the kube-apiserver and the health-check
ports of the member pods, and ingress for metrics and kubelet probes. The
chart prints a ready-to-adapt `NetworkPolicy` in its install notes
(`helm get notes <release>`; source:
[NOTES.txt](../charts/hcloud-fip-announcer/templates/NOTES.txt)).

## Verifying images and charts

Images and the chart are signed with cosign (keyless, GitHub Actions OIDC) and
carry an SBOM attestation.

```sh
cosign verify ghcr.io/tobiasmende/hcloud-fip-announcer:<version> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/TobiasMende/hcloud-fip-announcer/\.github/workflows/release\.yml@refs/(heads/main|tags/v.+)$'
```

The same command works for the chart at
`ghcr.io/tobiasmende/charts/hcloud-fip-announcer:<version>`. Pin images by
digest in production.

## Reporting vulnerabilities

See [SECURITY.md](../SECURITY.md).
