# Install the operator

This guide shows you how to install the hermes-operator into a Kubernetes cluster via Helm or raw kustomize, and how to upgrade and uninstall.

## Prerequisites

- Kubernetes 1.29+.
- For external dashboard exposure: an Ingress controller (nginx, traefik, etc.).
- For ServiceMonitor scraping: the Prometheus Operator's `monitoring.coreos.com/v1` CRDs.

Validation runs at the API server via the CRD's own OpenAPI schema and `x-kubernetes-validations` rules. There is no admission webhook, so cert-manager is not required.

## Helm (recommended)

```bash
helm install hermes-operator oci://ghcr.io/undermountaincc/charts/hermes-operator \
    --namespace hermes-operator-system --create-namespace \
    --set operator.allowedClusterRoles="cluster-admin,admin"
```

`operator.allowedClusterRoles` is the install-time allowlist gating `HermesAgent.spec.rbac.clusterRoleBindings[]`. Default empty, which is safe but disables cluster-scoped agent bindings entirely. See [The RBAC model](../concepts/rbac-model.md).

Verify the controller-manager came up:

```bash
kubectl -n hermes-operator-system rollout status deployment hermes-operator-controller-manager --timeout=120s
```

See [Reference: Helm values](../reference/helm-values.md) for every chart value.

### Common toggles

Prometheus Operator integration:

```bash
helm install hermes-operator ... \
    --set prometheus.serviceMonitor.enabled=true
```

OpenTelemetry tracing:

```bash
helm install hermes-operator ... \
    --set tracing.otlp.endpoint=otel-collector.observability.svc:4317
```

See [Enable Prometheus metrics + Grafana dashboards](metrics-dashboards.md) and [Send OpenTelemetry traces to a collector](opentelemetry.md).

## Plain kustomize

Apply the published bundle directly:

```bash
kubectl apply -k https://github.com/UndermountainCC/hermes-operator//config/default?ref=v0.1.0
```

Or pull the source and build locally:

```bash
git clone https://github.com/UndermountainCC/hermes-operator
cd hermes-operator
make deploy IMG=ghcr.io/undermountaincc/hermes-operator:v0.1.0
```

## Upgrade

```bash
helm upgrade hermes-operator oci://ghcr.io/undermountaincc/charts/hermes-operator \
    --namespace hermes-operator-system
```

CRDs are not auto-upgraded by `helm upgrade` (intentional, to avoid silently breaking stored CRs). When the CRD changes between operator versions, apply the new CRD manually:

```bash
kubectl apply -f https://raw.githubusercontent.com/UndermountainCC/hermes-operator/<new-tag>/config/crd/bases/hermes.k8s.undermountain.cc_hermesagents.yaml
```

!!! note "Upgrading from a release that depended on cert-manager"
    Releases before the webhook was removed left a `webhook-server-cert` (and sometimes `metrics-server-cert`) Secret behind, owned by the now-deleted cert-manager Certificate. The operator does not need them, but the Secrets linger until you clean up:

    ```bash
    kubectl -n hermes-operator-system delete secret webhook-server-cert metrics-server-cert --ignore-not-found
    ```

    Safe to run unconditionally: `--ignore-not-found` is a no-op on fresh installs. See also [Reference: Troubleshooting catalogue](../reference/troubleshooting.md).

## Uninstall

```bash
kubectl delete hermesagents --all -A
helm uninstall hermes-operator --namespace hermes-operator-system
kubectl delete crd hermesagents.hermes.k8s.undermountain.cc
```

PVCs are retained by default (`spec.storage.retainPolicy: Retain`). Delete remaining PVCs manually if you no longer need the agent state:

```bash
kubectl get pvc -A -l app.kubernetes.io/managed-by=hermes-operator
kubectl -n <ns> delete pvc <pvc-name>
```

See also [Upgrade and uninstall](upgrade-uninstall.md) for the standalone version of this section.

## Local development (no cluster)

```bash
make run
```

Runs the operator against whatever cluster your kubeconfig points at. Useful for fast iteration.
