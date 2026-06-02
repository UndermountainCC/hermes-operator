# Send OpenTelemetry traces to a collector

This guide shows you how to point the operator's OTLP gRPC tracer at a collector and what to filter on when the spans land. When the endpoint is unset, tracing is a no-op: zero overhead, no connection attempt, no startup latency.

## How to enable

### Helm

```bash
helm install hermes-operator oci://ghcr.io/undermountaincc/charts/hermes-operator \
    --set tracing.otlp.endpoint="otel-collector.observability.svc.cluster.local:4317" \
    --set tracing.otlp.serviceName="hermes-operator"
```

`tracing.otlp.serviceName` is optional. When unset, the operator reports `service.name=hermes-operator` on every span (the binary's built-in default).

### Raw kustomize / `make deploy`

Uncomment the `env:` block in `config/manager/manager.yaml` and re-deploy.

### Env vars the operator honors

Standard OTel:

| Env var | Effect |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP gRPC endpoint (required to enable tracing). |
| `OTEL_SERVICE_NAME` | `resource.service.name` on every span (defaults to `hermes-operator`). |
| `OTEL_RESOURCE_ATTRIBUTES` | Additional resource attributes (comma-separated `k=v` pairs, standard OTel). |
| `OTEL_EXPORTER_OTLP_HEADERS` | Outbound headers, used by Honeycomb / Grafana Cloud for auth. |

## Collector endpoints

The operator speaks OTLP gRPC. Common backends:

| Backend | Endpoint format |
|---|---|
| OTel Collector (in-cluster) | `otel-collector.observability.svc.cluster.local:4317` |
| Grafana Tempo | `tempo.observability.svc:4317` |
| Jaeger (via OTel Collector) | route OTLP → Jaeger via the collector's `jaeger` exporter |
| Honeycomb (direct OTLP) | `api.honeycomb.io:443` + `OTEL_EXPORTER_OTLP_HEADERS=x-honeycomb-team=…` |
| Grafana Cloud | host from your stack settings + `OTEL_EXPORTER_OTLP_HEADERS=Authorization=Basic …` |

If your tracing backend doesn't speak OTLP natively, run the [OTel Collector](https://opentelemetry.io/docs/collector/) in front of it as a translation layer.

## Span shape

Every reconcile produces a tree:

```
hermesagent.Reconcile (root)
├─ Reconcile.EnsureFinalizer
├─ Reconcile.PVC
├─ Reconcile.ServiceAccount
├─ Reconcile.SecretsValidation
├─ Reconcile.RBAC
├─ Reconcile.Deployment
├─ Reconcile.Service
├─ Reconcile.DashboardService
├─ Reconcile.DashboardIngress
├─ Reconcile.NetworkPolicy
├─ Reconcile.Status
│  └─ Reconcile.DashboardProbe   # if spec.dashboard.enabled
└─ Reconcile.HandleDeletion      # when CR is being deleted
```

Every span carries:

- `hermesagent.name`
- `hermesagent.namespace`

Span Status: `Ok` on clean return, `Error` with the error message on any non-nil error.

### Span events (timeline annotations)

- `Reconcile.PhaseTransition`: on `Reconcile.Status` when Phase changes. Attributes: `from`, `to`.
- `Reconcile.RBACRejected`: on `Reconcile.RBAC` when a `ClusterRoleBinding` references a role not in the `--allowed-cluster-roles` allowlist. Attribute: `roleName`.
- `Reconcile.DashboardProbeFailed`: on both `Reconcile.DashboardProbe` (child) and `Reconcile.Status` (parent) when the dashboard `/api/status` probe errors. Attribute: `error`.

These pinpoint exactly when a transition or failure happened, without requiring log correlation.

## Viewing traces

- **Jaeger UI**: filter on `service.name = hermes-operator`. Reconciles for a specific CR: filter `hermesagent.name = <name>` AND `hermesagent.namespace = <ns>`.
- **Grafana Tempo**: TraceQL: `{service.name="hermes-operator" && resource.hermesagent.name="<name>"}`.
- **Honeycomb / Grafana Cloud**: same `service.name` filter; `hermesagent.*` are facet filters automatically.

## Failure modes

- **Malformed endpoint URL**: the operator pod fails fast at startup with `unable to initialize OpenTelemetry tracing`, rather than silently dropping spans for the pod's lifetime.
- **Unreachable endpoint**: the OTel SDK retries silently in the background; reconciles proceed normally. There is no operator-side metric for export failures today (the OTel SDK logs to stderr at WARN level); watch the operator pod's logs for `otel` entries.
- **Tracing disabled, sidecar auto-instrumentation enabled**: safe. When `OTEL_EXPORTER_OTLP_ENDPOINT` is empty, the global TracerProvider stays no-op, so a sidecar's auto-instrumentation can run independently.

## Shutdown semantics

10s dial timeout on connection setup. SIGTERM-bounded shutdown flushes the last batch of spans best-effort within 5s. The pod is going away regardless, but the operator tries not to lose the last reconcile.

