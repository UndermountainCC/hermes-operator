# hermes-operator

Public-OSS Kubernetes operator for Hermes agent (Nous Research). Apache 2.0.

## Hard invariants

- **SSA, never Get-then-Update.** `r.Patch(ctx, desired, client.Apply, client.ForceOwnership, client.FieldOwner("hermes-operator"))`. Get-then-Update races kube-controller-manager and produces "Operation cannot be fulfilled ... the object has been modified" conflicts in real K8s. `applyObject` sets ownerRef; `applyBindingSSA` doesn't (RBAC bindings can't have cross-ns/cluster-scoped ownerRef). **When you need conditional logic (e.g., conditional ownerRef per `RetainPolicy`), mutate the desired object before the Patch — never fall back to Get-then-Update.** Phase 2 Task 18 regressed this in `reconcilePVC` (needed conditional ownerRef, reached for Get-then-Update, reintroduced PVC `Operation cannot be fulfilled` errors); Phase 3 QA fixed it back to SSA + conditional ownerRef-before-patch.
- **Status uses merge-patch.** `r.Status().Patch(ctx, agent, client.MergeFrom(original))` — same anti-conflict reason.
- **PVC sovereign post-first-boot.** No operator writes under `$HERMES_HOME` after creation. `RetainPolicy=Retain` is default; no ownerRef. Only set ownerRef when `Delete`.
- **RBAC reference-only.** CRD names existing Roles; operator creates bindings, never roles. CRBs gated by install-time `--allowed-cluster-roles` allowlist (default empty).
- **No Hermes-app fields in CRD.** Provider/gateway quirks go through `[]corev1.EnvVar` bags inside `gateways[].env` / `llmProviders[].env`. CRD covers K8s lifecycle only.
- **RBAC drift correction matches on `hermes.undermountain.cc/rbac-source: spec.rbac`.** Operator-internal bindings (`hermes-<name>-self`, `hermes-<name>-exec`) MUST NOT carry that label or drift correction in `reconcileRBAC` deletes them every reconcile (create/delete hot loop). `agentRBACSpecLabels` adds the marker; `agentLabels` doesn't. Test: `TestAgentRBACSpecLabels_OperatorInternalBindingsHaveNoMarker`.
- **`spec.execBackend: kubernetes` requires BOTH halves.** `exec_rbac.go` provisions the per-agent Role + RoleBinding + session SA. `env.go` stamps `TERMINAL_ENV=kubernetes` + `TERMINAL_KUBERNETES_POD_SA=hermes-<name>-session` + `TERMINAL_KUBERNETES_NAMESPACE` (Downward API). Without the env stamping, the agent silently falls back to `TERMINAL_ENV=local` and the RBAC is dormant.

## Test strategy

- envtest does NOT run kube-controller-manager — rv-conflict races don't reproduce naturally. For race-class regressions, write tests that inject the race via goroutine (see `resilience` spec in `hermesagent_controller_test.go`) AND run kind smoke.
- Tests asserting on log content (zap observer in `suite_test.go`) must scope filter to their own CR name. Other tests may leave failing CRs whose ongoing reconcile errors pollute the shared observer.
- **envtest misses class-of-bugs that need kube-proxy / real PIDs / real ownership:** PVC file permissions (container UID), Service endpoint filtering (pod readiness gating), shared-PID-namespace effects, real reconcile timing across multiple cycles. Run `make test-e2e` (operator-sdk scaffolded suite in `test/e2e/`) before pushing changes to reconciler logic, CRDV/VAP rules, or the sidecar shape.

## Local dev quirks

- `lsof -ti:8081 | xargs kill -9` — clear stale `make run` if port 8081 is "already in use".
- `kind create cluster --name hermes-test-$RANDOM` — parallel-safe naming for concurrent agents.
- Image registry must be lowercase: `ghcr.io/undermountaincc/hermes-operator` (Docker rejects uppercase, hardcoded in `release.yml`).
- Helm install conflicts with prior `make install` CRD: relabel + annotate for Helm adoption (`kubectl annotate crd ... meta.helm.sh/release-name=hermes-operator meta.helm.sh/release-namespace=hermes-operator-system --overwrite`).
- Operator's own SA needs `cluster-admin` to grant `cluster-admin` to agents (K8s RBAC escalation prevention). Manual `ClusterRoleBinding` required at install for high-privilege agent bindings.

## Known gaps (CHANGELOG `Known issues / follow-ups`)

- v1alpha1 implementation complete; ready for internal alpha use.

## Upstream-Hermes interop gotchas (full triage in `docs/operator/install.md`)

- **DeepSeek/non-Anthropic providers** return HTTP 400 because upstream `cli-config.yaml.example` ships `model.default: anthropic/claude-opus-4.6`. `HERMES_INFERENCE_MODEL` env var is oneshot-only (no effect on `hermes gateway run`). Fix: `kubectl exec deployment/hermes-<name> -- sed -i 's|anthropic/claude-opus-4.6|<provider-model>|' /opt/data/config.yaml && kubectl rollout restart deployment/hermes-<name>`.
- **Discord gateway crashloops with `PrivilegedIntentsRequired`.** Toggle MESSAGE CONTENT INTENT at `https://discord.com/developers/applications/<app>/bot`. One-time per bot.
- **`hermes` binary at `/opt/hermes/.venv/bin/hermes`** — not on default `$PATH`. Exec probes and `kubectl exec` commands must use the absolute path.
- **`hermes gateway status` always exits 0** — exec probes must `grep -q '✓ Gateway is running'` to synthesize the health signal. No `--json`/`--quiet`/`--exit-code` flags exist.
- **Gateway holds `fcntl.flock` on `$HERMES_HOME/gateway.lock`** — Deployment must be `replicas: 1` + `strategy: Recreate`. RollingUpdate crashloops the new pod on the old's flock.
- **Dashboard sidecar requires `pod.spec.shareProcessNamespace: true`** — upstream's dashboard uses PID-based gateway-liveness detection. Without shared PID namespace, dashboard reports `gateway_running: false` even when gateway is healthy.
- **Dashboard sidecar `securityContext.RunAsUser: 10000`** is mandatory when overriding `Container.Command`. Bypasses the upstream entrypoint's `gosu hermes` drop; otherwise dashboard writes `/opt/data` as root and the gateway crashes with `PermissionError`.
- **Never pass `--tui` to dashboard sidecar** — spawns a second hermes process on shared `$HERMES_HOME`, contends with gateway flock.
- **Dashboard `/api/status` is unauth by design** (operator probes it directly). Admin endpoints `/api/config`, `/api/env`, `/api/cron/jobs` are gated by an internal ephemeral session token (no env var to set). External auth: user-managed Ingress annotations.
- **Dashboard Service uses `publishNotReadyAddresses: true`** — observability must survive gateway outages. Default Service endpoint filtering hides the dashboard precisely when it's most useful.

## Common operations

- Bump golangci-lint: update BOTH `.github/workflows/ci.yml` (`golangci-lint-action@v8`) AND `Makefile`'s `GOLANGCI_LINT_VERSION`. v2+ module path: `github.com/golangci/golangci-lint/v2/cmd/golangci-lint`.
- Release: `git tag vX.Y.Z && git push origin vX.Y.Z`. Multi-arch builds are parallel (amd64+arm64 separate jobs); Dockerfile uses `--platform=$BUILDPLATFORM` for Go cross-compile (avoids QEMU). Pipeline ~6min total.
- New reconciled child resource: write `desiredXxx`, call `r.applyObject(ctx, agent, desired)`. Add `kubebuilder:rbac` marker + `Owns(...)` in `SetupWithManager`.
- New status field: extend `HermesAgentStatus`, populate in `computeStatus`. Ensure `statusEqual` accounts for it or the no-op-check skips updates.
- **CI uses `ucc-org-runners` (org self-hosted).** Affects all four workflows (`ci.yml`, `test-e2e.yml`, `docs.yml`, `release.yml`). Lint job needs `args: --timeout=10m` and e2e `Makefile` needs `-timeout 30m` to fit the runner.

## Public docs site (`docs/docs/`)

Diátaxis structure: **Tutorials / How-to / Concepts / Reference / Project**. Strict rules: NO phase numbers in titles or body, NO links to `docs/research/` (intentionally unpublished), NO contributor-doc content (those go in repo-root `CONTRIBUTING.md`). The `llmDefaultProvider` precedence caveat is a load-bearing user-facing fact and must survive any docs rewrite.

## Phase status

See `CHANGELOG.md` `[Unreleased]` for current phase status.
