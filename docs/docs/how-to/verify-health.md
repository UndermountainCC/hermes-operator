# Verify a HermesAgent is healthy

This guide shows you the four checks that confirm an agent is fully working end-to-end, from "the CR landed in etcd" to "a real message round-trip succeeded."

Run these in order. If any check fails, the next one is meaningless until you fix it. For decision-tree-style triage, see [Troubleshoot an agent that isn't Ready](troubleshoot-not-ready.md).

## 1. CR-level: `status.phase` is `Ready`

```bash
kubectl -n hermes get hermesagent my-agent
```

```
NAME       PHASE   IMAGE                                            AGE
my-agent   Ready   docker.io/nousresearch/hermes-agent:v2026.4.30   2m
```

If `PHASE` is anything other than `Ready`, jump to [Troubleshoot an agent that isn't Ready](troubleshoot-not-ready.md).

Also check the conditions:

```bash
kubectl -n hermes get hermesagent my-agent -o jsonpath='{.status.conditions}' | jq
```

Expect `PodReady=True`, `SecretsResolved=True`, `RBACSynced=True`. If the dashboard sidecar is enabled, also expect `GatewaysReady=True`. See [Lifecycle, phases, and conditions](../concepts/lifecycle.md) for what each condition means.

## 2. Pod-level: the pod is Ready and the gateway is running

```bash
kubectl -n hermes get pod -l app.kubernetes.io/instance=my-agent
```

```
NAME                            READY   STATUS    RESTARTS   AGE
hermes-my-agent-7d8c4f9b5-xqz4t 1/1     Running   0          90s
```

`READY` should be `1/1` (or `2/2` if the dashboard sidecar is enabled). `RESTARTS` should be 0 for a fresh deploy; non-zero is not always wrong, but worth a glance at logs to see what restarted.

Check the agent logs for the readiness signal:

```bash
kubectl -n hermes logs deployment/hermes-my-agent -c agent --tail=50 | grep 'Gateway'
```

Expect `✓ Gateway is running`. The exec probe greps for this exact string.

## 3. Gateway-level: the messaging platform is connected

For each gateway entry on the CR, check the platform-side connection. The exact log line varies:

- **Discord**: `Logged in as <bot-name>` after token validation.
- **Telegram**: `Bot @<handle> connected` or similar.
- **WhatsApp**: a QR-code prompt or the saved-session reuse line.

If the dashboard sidecar is enabled, the same information is in `status.gateways[]`:

```bash
kubectl -n hermes get hermesagent my-agent -o jsonpath='{.status.gateways}' | jq
```

```json
[
  {
    "type": "discord",
    "ready": true,
    "state": "connected",
    "lastProbedAt": "2026-05-30T12:34:56Z"
  }
]
```

Without the dashboard, you only see this from the logs.

## 4. End-to-end: send a real message and get a reply

This is the only check that exercises the full path: Discord/Telegram/WhatsApp API → upstream gateway → LLM provider → reply.

From your messaging platform, send the agent a direct message:

- **Discord**: DM the bot, or `@mention` it in a channel where it has `View Channels` + `Send Messages` permissions.
- **Telegram**: search for the bot by handle and send `/start` or any message.
- **WhatsApp**: message the bot's number.

A reply should come back within a few seconds. If it doesn't, check agent logs for a stack trace from the LLM call (auth, model name, rate limit, network). A common one: `HTTP 400 ... but you passed anthropic/claude-opus-4.6` when using a non-Anthropic provider. See [Change the LLM provider for an agent](change-llm-provider.md).

## Optional: dashboard `/api/status` walk-through

If `spec.dashboard.enabled: true`:

```bash
kubectl -n hermes port-forward svc/hermes-my-agent-dashboard 9119:9119
curl -s http://localhost:9119/api/status | jq
```

```json
{
  "gateway_running": true,
  "gateway_platforms": {
    "discord": { "state": "connected", "error_message": "" }
  }
}
```

`gateway_running: true` AND every platform `state: connected` means the agent is fully operational.

## See also

- [Troubleshoot an agent that isn't Ready](troubleshoot-not-ready.md) — when one of the checks above fails.
- [Lifecycle, phases, and conditions](../concepts/lifecycle.md) — what each status field means.
- [Reference: Troubleshooting catalogue](../reference/troubleshooting.md) — symptom-keyed lookup.
