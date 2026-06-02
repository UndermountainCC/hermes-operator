# Known Hermes upstream behaviours

This page catalogues upstream Hermes behaviours that catch operator users off-guard. Everything below is validated against `docker.io/nousresearch/hermes-agent:v2026.4.30`.

!!! warning "Caveat: changing `spec.llmDefaultProvider` may not switch providers at runtime"
    Upstream resolves the active provider from `$HERMES_HOME/config.yaml`'s `model.provider` BEFORE falling back to the `HERMES_INFERENCE_PROVIDER` env var that the operator stamps. Because `config.yaml` is seeded with a concrete value on first boot, editing `spec.llmDefaultProvider` does not switch the provider on a running agent. See [Change the LLM provider for an agent](../how-to/change-llm-provider.md) for the workarounds.

## `model.default` is hardcoded to `anthropic/claude-opus-4.6` in the bundled config

Upstream's bundled `cli-config.yaml.example` ships with `model.default: anthropic/claude-opus-4.6`. When Hermes copies its bundled config to `$HERMES_HOME/config.yaml` on first boot, that is what lands on the PVC.

If you set `spec.llmDefaultProvider: deepseek` (or any non-Anthropic provider), the agent routes requests to that provider's endpoint but keeps the wrong model name. DeepSeek replies with:

```
HTTP 400 The supported API model names are deepseek-v4-pro or deepseek-v4-flash,
but you passed anthropic/claude-opus-4.6
```

The `HERMES_INFERENCE_MODEL` env var does not help: upstream consults it only in `hermes -z` oneshot mode, not in `hermes gateway run` (the mode the operator runs).

Workarounds and the full how-to: [Change the LLM provider for an agent](../how-to/change-llm-provider.md).

The operator does not patch this. The PVC is sovereign; see [PVC sovereignty](storage.md).

## Discord requires MESSAGE CONTENT INTENT

Discord bots crashloop with `discord.errors.PrivilegedIntentsRequired` if the bot's MESSAGE CONTENT INTENT is not toggled on in the Discord developer portal. One-time per bot, set at:

```
https://discord.com/developers/applications/<app>/bot
```

Find **Privileged Gateway Intents** and toggle **MESSAGE CONTENT INTENT** to on. See [Set up a Discord bot](../how-to/setup-discord-bot.md) for the full walkthrough.

## `hermes` lives at `/opt/hermes/.venv/bin/hermes`, not on `$PATH`

The `hermes` binary is not on `$PATH` inside the container. Any `kubectl exec` invocation and the operator's own readiness probe must use the absolute path:

```bash
kubectl -n hermes exec deployment/hermes-my-agent -- \
    /opt/hermes/.venv/bin/hermes gateway status
```

## `hermes gateway status` always exits 0

There is no `--json`, `--quiet`, or `--exit-code` flag. The Rich-text output is the truth source:

```
✓ Gateway is running
```

Exec probes must `grep -q '✓ Gateway is running'` to synthesize the health signal. The operator's readiness probe does exactly this.

## `hermes gateway run` does not bind an HTTP server

This is why the agent's readiness probe is an exec probe rather than an HTTP probe. The only HTTP surface in the agent container is the dashboard subcommand, which runs as a separate process and is exposed as the opt-in dashboard sidecar.

## The `gateway.lock` flock: why `replicas: 1` and `strategy: Recreate`

The gateway holds `fcntl(LOCK_EX|LOCK_NB)` on `$HERMES_HOME/gateway.lock` for the lifetime of the process:

- A second `hermes gateway run` process started against the same `$HERMES_HOME` crashloops immediately with `Gateway already running (PID <n>)`.
- The Deployment must be `replicas: 1` plus `strategy: Recreate`. A RollingUpdate strategy crashloops the new pod on the old pod's flock every time.

The operator hardcodes both. `replicas` is not exposed as a spec field.

## Signal handling

| Signal | Effect |
|---|---|
| `SIGTERM` | Drains active turns up to `agent.restart_drain_timeout` (180s default), closes SessionDB, releases `gateway.lock`, writes `.clean_shutdown`. |
| `SIGINT` | Same as SIGTERM but always exits 0. |
| `SIGUSR1` | Graceful drain + exit-with-restart-code (intended for service-manager restart). |
| `SIGHUP` | **Not handled.** Process dies abruptly. No drain. Sessions marked `resume_pending` on next boot. |
| `SIGKILL` | Uncatchable. No drain. Next boot calls `suspend_recently_active()`. |

`tini -g` (in the container) forwards SIGTERM from Kubernetes to the gateway process group. The operator's `terminationGracePeriodSeconds: 210` budgets the 180s drain plus a 30s teardown buffer. See [Lifecycle, phases, and conditions](lifecycle.md) for the full breakdown.

There is no `hermes gateway reload` subcommand and no config-file watching. `config.yaml` is read once at startup and is authoritative for the process lifetime. Only `~/.hermes/.env` is reloaded per-turn (for rotated credentials).

## The dashboard requires a shared PID namespace

Upstream's `hermes dashboard` uses PID-based gateway-liveness detection. Kubernetes containers in the same pod have isolated PID namespaces by default, so without `pod.spec.shareProcessNamespace: true` the dashboard always reports `gateway_running: false`, even on a perfectly healthy agent. From [upstream's Docker docs](https://hermes-agent.nousresearch.com/docs/user-guide/docker):

> Running it as a separate container is not supported: the dashboard's gateway-liveness detection requires a shared PID namespace with the gateway process.

The operator stamps `shareProcessNamespace: true` whenever the dashboard sidecar is enabled. The isolation reduction is acceptable for a single-tenant pod and is unavoidable as long as upstream stays PID-based.

## The dashboard's auth model is ephemeral and operator-out-of-the-loop

- `/api/status` is unauthenticated by upstream design. The operator polls it directly; no token plumbing is required.
- The SPA at `/` and the admin endpoints (`/api/config`, `/api/env`, `/api/cron/jobs`) are gated by an ephemeral session token generated by the dashboard at process start (`secrets.token_urlsafe(32)`) and injected into the rendered SPA HTML. Only the browser session that loaded the page can talk to the protected endpoints.
- There is no env var that overrides this token. The operator does not provision a token Secret.

External-edge auth (oauth2-proxy, nginx `auth-url`, traefik forwardAuth, etc.) is your responsibility. See [Expose the dashboard externally with auth](../how-to/expose-dashboard.md).

## See also

- [Lifecycle, phases, and conditions](lifecycle.md) — termination grace, restart paths.
- [PVC sovereignty](storage.md) — why the operator does not patch `config.yaml` for you.
- [Reference: Troubleshooting catalogue](../reference/troubleshooting.md) — symptom-keyed lookup.
