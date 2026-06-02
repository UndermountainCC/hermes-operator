# Enable the dashboard sidecar

This guide shows you how to turn on upstream's `hermes dashboard` as a sidecar container in your agent Pod. Enabling the dashboard gives you an HTTP UI + REST API for the running agent and populates `status.gateways[]` on the CR via 30s polling of the sidecar's `/api/status` endpoint.

For exposing the dashboard outside the cluster with auth, follow up with [Expose the dashboard externally with auth](expose-dashboard.md).

## What you get

- A `dashboard` sidecar container running `hermes dashboard --insecure --host 0.0.0.0 --no-open` on port 9119.
- `pod.spec.shareProcessNamespace: true` on the agent Pod (mandatory; upstream uses PID-based liveness detection).
- A `Service/hermes-<name>-dashboard` (ClusterIP, port 9119, `publishNotReadyAddresses: true` so observability survives gateway outages).
- `/api/status` (unauthenticated): JSON with per-platform connection state. The operator polls this directly.
- The SPA at `/` and admin endpoints (`/api/config`, `/api/env`, `/api/cron/jobs`): gated by an ephemeral session token the dashboard embeds in the rendered HTML.

## Enable it

```yaml
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: my-agent, namespace: hermes }
spec:
  image: docker.io/nousresearch/hermes-agent:v2026.4.30
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 50Gi } }
  dashboard:
    enabled: true
```

Apply and the operator adds the sidecar + Service on the next reconcile.

## Reach it via port-forward

```bash
kubectl -n hermes port-forward svc/hermes-my-agent-dashboard 9119:9119
open http://localhost:9119/
```

The SPA loads with the session token embedded, so the browser session can talk to admin endpoints. Closing the tab effectively logs you out.

## Inspect per-gateway status from kubectl

Once the dashboard is enabled, `status.gateways[]` populates within the 30s polling window:

```bash
kubectl -n hermes get hermesagent my-agent -o jsonpath='{.status.gateways}' | jq
```

Each entry has `type`, `ready`, `state` (`connecting` / `connected` / `retrying` / `fatal` / `disconnected`), and `message` (populated for fatal states).

## Expose externally

Set `spec.dashboard.ingress.enabled: true` and the operator reconciles an Ingress. You must layer external authentication in front of it; see [Expose the dashboard externally with auth](expose-dashboard.md). The CRD documents the requirement but does not enforce specific annotations.

## Tradeoffs to understand

- **`shareProcessNamespace: true` is mandatory.** Every container in the Pod can see every other's process tree. This is a real isolation reduction, but acceptable in a single-tenant Pod and unavoidable as long as upstream stays PID-based for liveness detection.
- **Sidecar runs as UID 10000.** The operator stamps `securityContext.runAsUser: 10000` on the sidecar so it does not write to `/opt/data` as root, which would crash the gateway.
- **The operator hardcodes safe flags.** Do not pass `--tui` to `hermes dashboard`: it would spawn a second hermes process contending with the gateway over `gateway.lock`. The operator's flags are not user-overridable for this reason.

## See also

- [Expose the dashboard externally with auth](expose-dashboard.md) — Ingress with TLS and an auth proxy.
- [Reference: HermesAgent API](../reference/api-reference.md#hermesagentdashboardspec) — every dashboard field.
- [Known Hermes upstream behaviours](../concepts/upstream-behaviours.md) — the auth model and shared-PID rationale.
