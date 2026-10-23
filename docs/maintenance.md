# Maintenance

## Rebooting or replacing a node

Cordoning is the signal. When the node holding a group's Floating IPs is
cordoned, the group moves at once (the anti-flap interval does not apply) to
another eligible node. Cordon, wait for `status.node` to change, then take the
node down.

```sh
NODE=worker-1
kubectl cordon "$NODE"

# kubectl wait cannot express "!=", so poll every group the node holds
for group in $(kubectl get floatingipgroup -o json |
    jq -r --arg n "$NODE" '.items[] | select(.status.node == $n) | .metadata.name'); do
  until [ "$(kubectl get floatingipgroup "$group" -o jsonpath='{.status.node}')" != "$NODE" ]; do
    sleep 2
  done
  kubectl get floatingipgroup "$group"
done

# reboot, upgrade or delete the node here

kubectl uncordon "$NODE"
```

If you know the target node, `kubectl wait` works directly:

```sh
kubectl wait floatingipgroup/ingress --for=jsonpath='{.status.node}'=worker-2 --timeout=60s
```

Check that the group is healthy afterwards:

```sh
kubectl wait floatingipgroup/ingress --for=condition=Ready --timeout=60s
```

Notes:

- There is no failback. After uncordoning, the IPs stay where they are until
  that node stops being eligible.
- If no other node is eligible, the group becomes `Degraded` (`NoEligibleNode`)
  and the IPs stay on the cordoned node. Check `kubectl get floatingipgroup`
  before draining.
- A move takes a few seconds (the Hetzner assignment is about 6-7 s); expect a
  short interruption for new connections.
- Drain only after the move has finished; the cordon move happens before the
  disruption, while pods leaving the holder would make it ineligible too late.

## Upgrading the controller

```sh
helm upgrade hcloud-fip-announcer \
  oci://ghcr.io/tobiasmende/charts/hcloud-fip-announcer \
  --version <version> --namespace fip-announcer --reuse-values
```

- Helm does not upgrade CRDs from a chart's `crds/` directory. Apply the new
  CRD first when release notes say it changed:
  `helm pull oci://ghcr.io/tobiasmende/charts/hcloud-fip-announcer --version <version> --untar`
  then `kubectl apply -f hcloud-fip-announcer/crds/`.
- The Deployment runs two replicas with a PodDisruptionBudget (`minAvailable: 1`).
  A rolling update hands the leader lease over within a few seconds; the
  Floating IPs stay assigned while the controller is unavailable, only
  reactions to failures pause.
- The API is `v1alpha1`: read the release notes of every upgrade.

## What to watch

- `kubectl get floatingipgroup`: node and reason; `kubectl describe` for
  conditions and events.
- Alerts (chart `prometheusRule.enabled`): `GroupDegraded`, `NoEligibleNode`,
  `HcloudAPIErrors`, `NoLeader`, `FrequentMoves`.
- `fip_announcer_moves_total` by reason: repeated `ProbeFailed` points at a
  failing health check, repeated `External` at someone moving IPs by hand.
- The Grafana dashboard (`dashboard.enabled`).
- Logs: every move is one line (`Moved Floating IPs` with group, ips, from, to, reason).
