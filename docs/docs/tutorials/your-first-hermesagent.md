# Your first HermesAgent (Discord)

In this tutorial, you will deploy a real Discord-backed Hermes agent and watch it become talkable-to. About five minutes of YAML once your credentials and bot are set up. Assumes the operator is already installed; if not, see [Quickstart](quickstart.md).

## Before you start

You need a Discord application with a bot user, the bot token in hand, and **MESSAGE CONTENT INTENT** enabled. If you have not set that up yet, work through [Set up a Discord bot](../how-to/setup-discord-bot.md) first. It takes about five minutes and will save you a crashloop.

## 1. Create credentials

```bash
kubectl create namespace hermes

kubectl -n hermes create secret generic my-agent-creds \
    --from-literal=DEEPSEEK_API_KEY=sk-... \
    --from-literal=DISCORD_BOT_TOKEN=...
```

The operator validates every `secretKeyRef` and `envFrom.secretRef` in the spec before it creates the Pod. Missing secrets land as `SecretsResolved=False` on the CR rather than as a `CreateContainerConfigError` pod event you have to dig out.

## 2. Apply the CR

```yaml title="my-agent.yaml"
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata:
  name: my-agent
  namespace: hermes
spec:
  image: docker.io/nousresearch/hermes-agent:v2026.4.30

  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources:
        requests: { storage: 50Gi }
    # retainPolicy: Retain  # default — PVC survives CR deletion

  resources:
    requests: { cpu: 500m, memory: 1Gi }
    limits:   { cpu: 2,    memory: 4Gi }

  llmDefaultProvider: deepseek
  llmProviders:
    - name: deepseek
      models: [deepseek-v4-pro, deepseek-v4-flash]
      env:
        - name: DEEPSEEK_API_KEY
          valueFrom:
            secretKeyRef: { name: my-agent-creds, key: DEEPSEEK_API_KEY }

  gateways:
    - type: discord
      env:
        - name: DISCORD_BOT_TOKEN
          valueFrom:
            secretKeyRef: { name: my-agent-creds, key: DISCORD_BOT_TOKEN }
```

```bash
kubectl apply -f my-agent.yaml
```

## 3. Field-by-field

| Field | Purpose |
|---|---|
| `spec.image` | Container image for the agent. Pin to a digest (`@sha256:…`) in production. |
| `spec.storage.persistentVolumeClaim` | Native `corev1.PersistentVolumeClaimSpec`. The operator creates a PVC matching this spec and mounts it at `/opt/data` (`$HERMES_HOME`). |
| `spec.storage.retainPolicy` | `Retain` (default) keeps the PVC when the CR is deleted. `Delete` removes it; don't pick this for stateful agents. |
| `spec.resources` | Native `corev1.ResourceRequirements`. Applied to the agent container directly. |
| `spec.llmDefaultProvider` | Becomes `HERMES_INFERENCE_PROVIDER` on the container env. The CRD validates that it matches an entry in `spec.llmProviders[].name`. **Caveat:** upstream resolves the active provider from `$HERMES_HOME/config.yaml`'s `model.provider` BEFORE falling back to this env var, so editing the field on a running agent does not switch the provider. See [Change the LLM provider for an agent](../how-to/change-llm-provider.md). |
| `spec.llmProviders[].env` | Per-provider env vars, typically the API key via `secretKeyRef`. Operator concatenates these onto the container env. |
| `spec.gateways[].type` | Discriminator (`discord`, `telegram`, `whatsapp`, etc.). |
| `spec.gateways[].env` | Per-gateway env vars: tokens, allowlists, etc. |

For the full list, see [Reference: API](../reference/api-reference.md).

## 4. What the operator creates

From this single CR the operator reconciles:

- `PersistentVolumeClaim/hermes-my-agent`: sized + accessModes from `spec.storage.persistentVolumeClaim`. No ownerRef (PVC sovereignty).
- `ServiceAccount/hermes-my-agent`: referenced by the Pod.
- `Role/hermes-my-agent-self` + `RoleBinding/hermes-my-agent-self`: narrow self-introspection grant (read own spec/status, patch own Deployment for rollout-restart). See [The RBAC model](../concepts/rbac-model.md).
- `Deployment/hermes-my-agent`: `replicas: 1`, `strategy: Recreate`, exec probe `hermes gateway status | grep -q '✓ Gateway is running'`, `terminationGracePeriodSeconds: 210`.
- `Service/hermes-my-agent`: ClusterIP for in-cluster reach.

If `spec.dashboard.enabled=true`, also: a `dashboard` sidecar container, `Service/hermes-my-agent-dashboard`, and (when configured) an Ingress.

If `spec.networkPolicy.enabled=true`, also: a per-agent `NetworkPolicy` from `spec.networkPolicy.{ingress,egress,policyTypes}`.

## 5. Verify

```bash
kubectl -n hermes get hermesagent my-agent
# NAME     PHASE   IMAGE                                            AGE
# my-agent   Ready   docker.io/nousresearch/hermes-agent:v2026.4.30   46s

kubectl -n hermes describe hermesagent my-agent
# … Conditions: PodReady=True, SecretsResolved=True, RBACSynced=True

kubectl -n hermes logs deployment/hermes-my-agent -c agent --tail=30
```

The agent should print `✓ Gateway is running` and (for Discord) `Logged in as <bot-name>`. From your Discord server, DM the bot or `@mention` it in an allowed channel.

## Troubleshooting first boots

- **Phase stuck at `Bootstrap`**: check the `SecretsResolved` condition; some `secretKeyRef` could not be resolved.
- **Discord crashloops with `PrivilegedIntentsRequired`**: toggle MESSAGE CONTENT INTENT in the Discord developer portal. See [Set up a Discord bot](../how-to/setup-discord-bot.md).
- **DeepSeek (or any non-Anthropic provider) returns HTTP 400 on first message**: upstream's bundled `config.yaml` defaults `model.default` to an Anthropic model. See [Change the LLM provider for an agent](../how-to/change-llm-provider.md).
- More: [Troubleshoot an agent that isn't Ready](../how-to/troubleshoot-not-ready.md) and [Reference: Troubleshooting catalogue](../reference/troubleshooting.md).
