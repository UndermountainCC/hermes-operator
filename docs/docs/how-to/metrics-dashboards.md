# Enable Prometheus metrics + Grafana dashboards

This guide shows you how to wire the operator's `/metrics` endpoint into Prometheus via a ServiceMonitor and how to import the bundled Grafana dashboards. For metric names and labels, see [Reference: Metrics and labels](../reference/metrics.md).

## Enable the ServiceMonitor

The chart ships a `ServiceMonitor`, gated on the `prometheus.serviceMonitor.enabled` value (default `false`). Requires the Prometheus Operator's `monitoring.coreos.com/v1` CRDs pre-installed:

```bash
helm install hermes-operator oci://ghcr.io/undermountaincc/charts/hermes-operator \
    --set prometheus.serviceMonitor.enabled=true \
    --namespace hermes-operator-system
```

The chart fails fast if the CRDs are missing rather than installing a no-op resource.

For raw kustomize installs, uncomment the `../prometheus` entry in `config/default/kustomization.yaml` before `make deploy`.

## Import the Grafana dashboards

Two JSON dashboards live under `dashboards/` in the operator repo.

### `operator.json` (operator-side view)

Controller-runtime metrics across all reconciled CRs:

- Reconcile rate (`controller_runtime_reconcile_total`).
- Reconcile error rate.
- Workqueue depth.
- Reconcile latency p50 / p95 / p99.

Useful for capacity planning and spotting operator-pod-level regressions.

### `agent.json` (per-CR view)

Template variables: `$namespace`, `$name`. Panels:

- Phase (current and historical).
- Pod readiness over time.
- Per-gateway state (when dashboard sidecar enabled).
- Dashboard probe failure rate.

Useful for SREs investigating a single agent's behavior.

### Importing via Grafana UI

Go to **Dashboards → Import → Upload JSON** and pick a Prometheus datasource.

### Importing via ConfigMap (kube-prometheus-stack Grafana sidecar)

```bash
kubectl -n monitoring create configmap hermes-operator-dashboards \
    --from-file=operator.json=dashboards/operator.json \
    --from-file=agent.json=dashboards/agent.json
kubectl -n monitoring label configmap hermes-operator-dashboards grafana_dashboard=1
```

The Grafana sidecar discovers labelled ConfigMaps and loads them automatically.

## Set up alerts

The operator does not ship Prometheus alert rules. A starter set you can adapt:

```promql
# Phase=Degraded for >5 minutes
hermes_agent_phase{phase="Degraded"} == 1
# alert: HermesAgentDegraded, for: 5m

# Dashboard probe failure rate
rate(hermes_agent_dashboard_probe_failures_total[5m]) > 0
# alert: HermesAgentDashboardUnreachable, for: 10m

# Operator reconcile errors
rate(controller_runtime_reconcile_errors_total{controller="hermesagent"}[5m]) > 0
# alert: HermesOperatorReconcileErrors, for: 10m

# Reconcile latency degrading (often the dashboard probe is slow)
controller_runtime_reconcile_time_seconds{controller="hermesagent",quantile="0.99"} > 5
# alert: HermesOperatorReconcileLatencyHigh, for: 10m
```

## Forward compatibility

The dashboards depend on exact metric names and labels. Renaming a metric in a future operator release is a breaking change for downstream alerts. Pin your chart version if you depend on stable panel names.

## See also

- [Reference: Metrics and labels](../reference/metrics.md) — every emitted metric documented.
- [Send OpenTelemetry traces to a collector](opentelemetry.md) — the other observability surface.
