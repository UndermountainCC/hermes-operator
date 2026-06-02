# hermes-operator

![E2E](https://github.com/UndermountainCC/hermes-operator/actions/workflows/test-e2e.yml/badge.svg)

> **Status:** v1alpha1 implementation complete; ready for internal alpha use. Public API may change.

A Kubernetes operator for deploying and managing [Hermes](https://github.com/NousResearch/hermes-agent) AI agent instances. Define an agent declaratively via a `HermesAgent` custom resource; the operator reconciles the Pod, PVC, ServiceAccount, RBAC bindings, env composition, and per-gateway readiness status.

## Why use this

Hermes is stateful: each agent has a persistent home directory, paired credentials (Discord/Telegram/WhatsApp tokens), and accumulated memory. Running multiple agents on Kubernetes means managing one Deployment + PVC + Secrets + RBAC per agent. The operator collapses that to one CR per agent.

Defining an agent looks like:

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
  llmDefaultProvider: deepseek
  llmProviders:
    - name: deepseek
      env:
        - name: DEEPSEEK_API_KEY
          valueFrom:
            secretKeyRef: { name: my-creds, key: DEEPSEEK_API_KEY }
  gateways:
    - type: discord
      env:
        - name: DISCORD_BOT_TOKEN
          valueFrom:
            secretKeyRef: { name: my-creds, key: DISCORD_BOT_TOKEN }
        - { name: DISCORD_ALLOWED_USERS, value: "123456789" }
```

Apply, and the operator creates the Deployment + PVC + Service + RBAC + Service Account, plumbs env vars and secrets, and reports per-gateway readiness in `status`.

> **Note on `llmDefaultProvider`:** the field stamps `HERMES_INFERENCE_PROVIDER` on the container env, but upstream Hermes Agent resolves the active provider from `$HERMES_HOME/config.yaml`'s `model.provider` *before* falling back to the env var. The hermes-base image seeds `config.yaml` with `model.provider: deepseek` on first boot, so this field is best understood as a typo guard (validated against `llmProviders[].name`) rather than a runtime provider switch. To change providers in practice, edit `$HERMES_HOME/config.yaml` on the PVC and restart the pod.

## Install

### Helm (recommended)

```bash
helm repo add hermes-operator oci://ghcr.io/undermountaincc/charts
helm install hermes-operator hermes-operator/hermes-operator \
    --namespace hermes-operator-system --create-namespace
```

### kustomize

```bash
kubectl apply -k https://github.com/UndermountainCC/hermes-operator//config/default?ref=v0.1.0
```

See [docs/install.md](./docs/docs/install.md) for cert-manager prerequisite + per-platform notes.

## Documentation

- [Quickstart](https://undermountaincc.github.io/hermes-operator/) — install + first agent
- [API reference](https://undermountaincc.github.io/hermes-operator/api-reference/) — every field documented
- [Examples](https://undermountaincc.github.io/hermes-operator/examples/) — multi-gateway, fallback LLM providers, RBAC patterns

## E2E tests

The end-to-end test suite runs against a real Kind cluster. Use it locally before pushing changes that touch reconciler logic, the admission webhook, or the dashboard sidecar.

```bash
make test-e2e
```

Wall time: ~10-15 min (first run pulls a 2.5GB hermes-agent image; subsequent runs hit the local docker cache).

Override the cluster name (useful when running multiple suites in parallel):

```bash
KIND_CLUSTER=my-cluster make test-e2e
```

Skip cert-manager install (only if already present cluster-wide):

```bash
CERT_MANAGER_INSTALL_SKIP=true make test-e2e
```

CI runs this suite on every PR via the `E2E` workflow (badge at the top of this README).

## Project structure

- **`cmd/`** — operator entrypoint
- **`api/v1alpha1/`** — CRD type definitions (`HermesAgent`)
- **`internal/controller/`** — reconciler logic
- **`config/`** — kustomize manifests for install (CRD, RBAC, operator Deployment, webhook)
- **`charts/hermes-operator/`** — Helm chart wrapping kustomize
- **`docs/`** — Mkdocs site sources

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md). PRs welcome; please open an issue first for non-trivial changes.

## License

Dual-licensed:

- **Code** (everything outside the docs/prose below, including `examples/` and `config/samples/`) — Apache License 2.0. See [LICENSE](./LICENSE).
- **Documentation** (the `docs/` site, this README, and other prose) — Creative Commons Attribution 4.0 International (CC-BY-4.0). See [LICENSE-docs](./LICENSE-docs).

## Maintainers

[UndermountainCC](https://github.com/UndermountainCC). Not officially affiliated with NousResearch (the Hermes upstream project).
