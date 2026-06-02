# CLI flags

Flags accepted by the `hermes-operator` binary (`cmd/main.go`). Most are inherited from the operator-sdk scaffold; one is operator-specific.

## Operator-specific

| Flag | Default | Description |
|---|---|---|
| `--allowed-cluster-roles` | `""` (empty) | Comma-separated list of `ClusterRole` names permitted in `HermesAgent.spec.rbac.clusterRoleBindings[]`. Empty default disables cluster-scoped agent bindings entirely. Wired from Helm value `operator.allowedClusterRoles`. See [The RBAC model](../concepts/rbac-model.md). |

## Standard (operator-sdk scaffold)

| Flag | Default | Description |
|---|---|---|
| `--metrics-bind-address` | `"0"` | Address for the metrics endpoint. `:8443` for HTTPS, `:8080` for HTTP, `0` to disable. |
| `--metrics-secure` | `true` | Serve metrics over HTTPS. Set `--metrics-secure=false` for HTTP. |
| `--metrics-cert-path` | `""` | Directory containing the metrics server certificate. |
| `--metrics-cert-name` | `tls.crt` | Metrics certificate filename. |
| `--metrics-cert-key` | `tls.key` | Metrics certificate key filename. |
| `--health-probe-bind-address` | `:8081` | Liveness/readiness probe endpoint. |
| `--leader-elect` | `false` | Enable leader election; ensures only one active controller manager. |
| `--enable-http2` | `false` | Enable HTTP/2 on the metrics server. |

## Zap logging flags

The operator binds the standard zap logger flags (`--zap-devel`, `--zap-encoder`, `--zap-log-level`, `--zap-stacktrace-level`, `--zap-time-encoding`). Defaults are tuned for production JSON logs; set `--zap-devel=true` for human-readable dev output.

## Env vars

Beyond flags, the operator honors the standard OpenTelemetry env vars when tracing is enabled. See [Send OpenTelemetry traces to a collector](../how-to/opentelemetry.md).

| Env var | Effect |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP gRPC endpoint (required to enable tracing). |
| `OTEL_SERVICE_NAME` | `resource.service.name` (default `hermes-operator`). |
| `OTEL_RESOURCE_ATTRIBUTES` | Additional resource attributes. |
| `OTEL_EXPORTER_OTLP_HEADERS` | Outbound headers (Honeycomb / Grafana Cloud auth). |

## See also

- [Helm values](helm-values.md) — chart values wired through to these flags.
- [Install the operator](../how-to/install.md) — when to set them.
