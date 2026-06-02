# Contributing to hermes-operator

Thanks for your interest. PRs and issues welcome — for non-trivial changes, please open an issue first to discuss approach.

## Quick start

```bash
git clone https://github.com/UndermountainCC/hermes-operator
cd hermes-operator

# Run unit tests
make test-unit

# Run integration tests (envtest — downloads kube-apiserver + etcd binaries)
make test

# Lint
make lint

# Generate CRD manifests from kubebuilder markers (after editing api/ types)
make manifests
make generate
```

## Development environment

- Go 1.24+ (`go version`)
- operator-sdk v1.40+ (`operator-sdk version`)
- Docker (for building images)
- kind or another local Kubernetes for end-to-end smoke (`kind create cluster`)

Optional but useful:
- [`yq`](https://github.com/mikefarah/yq) v4+
- [`helm`](https://helm.sh) v3+ (for chart testing)
- [`golangci-lint`](https://golangci-lint.run) v1.60+ (`make lint` installs this if absent)

## Pull request flow

1. Fork the repo, branch from `main`.
2. Make changes. Add tests — unit tests for pure logic, envtest integration tests for reconciler behavior.
3. Run `make manifests generate test lint` locally before pushing.
4. Sign each commit with `-s` (DCO). PRs without sign-off are blocked by CI.
5. PR titles use [Conventional Commits](https://www.conventionalcommits.org/): `feat:`, `fix:`, `docs:`, `chore:`, `ci:`, `refactor:`, `test:`.
6. Squash on merge — one logical change per merged commit.

## DCO (Developer Certificate of Origin)

We use the DCO to certify contribution authorship. Every commit needs a `Signed-off-by: Your Name <email@example.com>` trailer (`git commit -s` adds it).

The DCO text is at [https://developercertificate.org](https://developercertificate.org). By signing off, you assert you wrote the change or have rights to contribute it under the project's Apache 2.0 license.

## Architecture

See [`docs/docs/`](./docs/docs/) for the docs site sources. High-level: a single Kubernetes controller reconciles `HermesAgent` CRs into Deployment + PVC + ServiceAccount + Service + RBAC. Env vars are composed from `spec.llmProviders[].env`, `spec.gateways[].env`, `spec.env`, plus operator-stamped identity fields.

The operator does NOT:
- Write under the agent's PVC after first boot (PVC sovereignty).
- Carry Hermes-application semantics in the CRD (no model selection, prompt tuning, routing — those are agent-runtime concerns).
- Build or push Hermes container images (separate concern).

User-facing design rationale lives in the [docs site](https://undermountaincc.github.io/hermes-operator/).

## Hard invariants

These are non-negotiable. If you find yourself wanting to violate one, that's a signal something else needs to change.

- **SSA, never Get-then-Update.** `r.Patch(ctx, desired, client.Apply, client.ForceOwnership, client.FieldOwner("hermes-operator"))`. Get-then-Update races kube-controller-manager and produces `Operation cannot be fulfilled` conflicts in real K8s. When you need conditional logic (e.g. conditional ownerRef per `RetainPolicy`), mutate the desired object before the Patch — never fall back to Get-then-Update.
- **Status uses merge-patch.** `r.Status().Patch(ctx, agent, client.MergeFrom(original))` — same anti-conflict reason.
- **PVC sovereign post-first-boot.** No operator writes under `$HERMES_HOME`. `RetainPolicy=Retain` is the default; no ownerRef. Only set ownerRef when `Delete`.
- **RBAC reference-only.** CRD names existing Roles; operator creates bindings, never roles. Cluster-scoped bindings are gated by install-time `--allowed-cluster-roles` allowlist. (The per-agent self-introspection Role is the one deliberate, hardcoded exception — see `internal/controller/self_rbac.go`.)
- **No Hermes-app fields in CRD.** Provider/gateway quirks go through `[]corev1.EnvVar` bags inside `gateways[].env` / `llmProviders[].env`. CRD covers K8s lifecycle only.
- **Native K8s types throughout.** `corev1.X`, `rbacv1.X`, `networkingv1.X`. Don't invent custom enums or structs where K8s has them — downstream tools (kustomize, helm, kubectl explain, k9s) understand the native types.

## What's stable vs. evolving

### Stable

- The hard invariants above.
- `HermesAgentSpec` top-level field names (`image`, `storage`, `llmProviders`, `gateways`, `rbac`, `dashboard`, `networkPolicy`). Sub-fields may move at minor version bumps.
- Custom metric names (`hermes_agent_*`). Renames are breaking changes for downstream dashboards.
- The exec readiness probe shape on the agent container.
- The dashboard sidecar contract (`/api/status` polling, `shareProcessNamespace`, port 9119).

### Evolving

- Status condition types and reason strings. New conditions may be added without deprecation cycles while we're in `v1alpha1`.
- Internal package layout under `internal/controller/`. Refactors happen.
- Helm chart structure (`hack/template-chart-image.py` post-processing). The right long-term answer is hand-written templates; not scheduled.

## Where to start when adding things

| Goal | Start here |
|---|---|
| Add a new reconciled child resource | `internal/controller/<name>.go` + `Owns(&newType{})` in `SetupWithManager`. Pattern: write `desiredXxx`, call `r.applyObject`. Add `kubebuilder:rbac` marker. |
| Add a new spec field | `api/v1alpha1/hermesagent_types.go`. `make manifests generate`. Wire into the relevant reconciler. Add CRD-level validation markers or `x-kubernetes-validations` for cross-field rules. |
| Add a new status condition | Constant in `hermesagent_types.go`. Populate in `computeStatus` (`internal/controller/status.go`). Ensure `statusEqual` accounts for it or no-op-check skips updates. |
| Add a new metric | `internal/metrics/agent_metrics.go`. Register with controller-runtime's metrics registry. Scrub in the finalizer to prevent ghost gauges. |

## Test strategy

- `make test-unit` — pure logic, fast.
- `make test` — envtest integration (downloads kube-apiserver + etcd binaries).
- `make test-e2e` — kind end-to-end via the operator-sdk Ginkgo suite. Run before pushing changes to reconciler logic or the sidecar shape.

envtest does NOT run kube-controller-manager — resource-version conflict races don't reproduce naturally. For race-class regressions, write tests that inject the race via goroutine AND run kind smoke.

envtest also misses a class of bugs that need kube-proxy / real PIDs / real ownership: PVC file permissions (container UID), Service endpoint filtering (pod readiness gating), shared-PID-namespace effects, real reconcile timing across multiple cycles. The e2e suite catches these.

Tests asserting on log content (zap observer in `suite_test.go`) must scope-filter to their own CR name. Other tests may leave failing CRs whose ongoing reconcile errors pollute the shared observer.

## Reporting bugs

Please use the issue templates at [.github/ISSUE_TEMPLATE/](./.github/ISSUE_TEMPLATE/). Include:
- `kubectl version` output.
- Operator version (`kubectl -n hermes-operator-system get deploy hermes-operator-controller-manager -o jsonpath='{.spec.template.spec.containers[0].image}'`).
- A minimal CR that reproduces.
- Relevant controller logs (`kubectl -n hermes-operator-system logs deploy/hermes-operator-controller-manager`).

## Security

DO NOT file public issues for security vulnerabilities. See [SECURITY.md](./SECURITY.md).
