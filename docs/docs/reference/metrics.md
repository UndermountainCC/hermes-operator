# Metrics and labels

Every metric the operator emits on its `/metrics` endpoint (HTTPS, port 8443, service-account-token auth). For the setup walkthrough, see [Enable Prometheus metrics + Grafana dashboards](../how-to/metrics-dashboards.md).

## Custom HermesAgent metrics

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `hermes_agent_phase` | gauge | `name, namespace, phase` | 1 for the active phase row; prior phase rows are deleted on transition so dashboards never read "Ready=1 AND Degraded=1". |
| `hermes_agent_pod_ready` | gauge | `name, namespace` | 1 when the agent Pod's `PodReady` condition is True. |
| `hermes_agent_gateway_ready` | gauge | `name, namespace, gateway_type` | 1 when the dashboard reports the platform as `connected`. Only populated when `spec.dashboard.enabled=true`. |
| `hermes_agent_dashboard_probe_failures_total` | counter | `name, namespace` | Cumulative dashboard probe failures (operator → sidecar `/api/status`). |

All rows are scrubbed by the CR's finalizer on delete; no ghost gauges remain after a HermesAgent is removed.

## Controller-runtime metrics

The operator also exposes every standard controller-runtime metric the operator-sdk scaffold provides: `controller_runtime_reconcile_total`, `controller_runtime_reconcile_errors_total`, `controller_runtime_reconcile_time_seconds`, workqueue depth, leader election state, and more. See [controller-runtime metrics docs](https://book.kubebuilder.io/reference/metrics-reference) for the full list.

## Label values

`phase` on `hermes_agent_phase` is one of `Bootstrap`, `Provisioning`, `Ready`, `Degraded`. `gateway_type` on `hermes_agent_gateway_ready` is the discriminator from `spec.gateways[].type` (e.g. `discord`, `telegram`, `whatsapp`).

## See also

- [Enable Prometheus metrics + Grafana dashboards](../how-to/metrics-dashboards.md) — ServiceMonitor + dashboard install.
- [Lifecycle, phases, and conditions](../concepts/lifecycle.md) — what each `phase` value means.
