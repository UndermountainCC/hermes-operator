# Change the LLM provider for an agent

This guide shows you how to switch the active LLM provider for a HermesAgent. There is a caveat that catches most users:

!!! warning "Caveat: `spec.llmDefaultProvider` is not the runtime switch"
    Upstream Hermes resolves the active provider from `$HERMES_HOME/config.yaml`'s `model.provider` **before** falling back to the `HERMES_INFERENCE_PROVIDER` env var that the operator stamps. Because `config.yaml` is seeded with a concrete value on first boot, **editing `spec.llmDefaultProvider` on a running agent does not switch the provider.** The field is best understood as a typo-guard (the CRD validates it matches an entry in `spec.llmProviders[].name`) rather than a runtime switch.

Three patterns actually work, ordered from quickest to cleanest.

## Option A: patch `config.yaml` on the PVC after first boot

The most direct fix. Edit the file in place, then restart the pod so the new process re-reads it.

```bash
# 1. See what's currently in config.yaml
kubectl -n hermes exec deployment/hermes-my-agent -- cat /opt/data/config.yaml | grep -A1 model

# 2. Patch it (example: switch from anthropic/claude-opus-4.6 to deepseek-v4-pro)
kubectl -n hermes exec deployment/hermes-my-agent -- \
    sed -i 's|anthropic/claude-opus-4.6|deepseek-v4-pro|' /opt/data/config.yaml

# 3. Make sure the matching provider credentials are in the agent's referenced Secret
# (e.g. DEEPSEEK_API_KEY for DeepSeek)

# 4. Restart the pod so the new config.yaml is read
kubectl -n hermes rollout restart deployment/hermes-my-agent
```

Update `spec.llmDefaultProvider` on the CR to match, so the env var matches the config file and the CRD-validated state is consistent.

## Option B: pre-seed `config.yaml` before first boot

If you have not yet applied the HermesAgent CR (or you have just deleted the PVC and started over), you can write `config.yaml` onto the PVC ahead of the agent's first boot. Once the file exists, the agent's bundled-config copy step skips it.

A one-shot Job that mounts the PVC and writes the file:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: seed-my-agent-config
  namespace: hermes
spec:
  template:
    spec:
      restartPolicy: OnFailure
      containers:
        - name: seed
          image: busybox:latest
          command:
            - sh
            - -c
            - |
              cat > /opt/data/config.yaml <<EOF
              model:
                provider: deepseek
                default: deepseek-v4-pro
              # ... rest of the keys you want pinned
              EOF
          volumeMounts:
            - { name: data, mountPath: /opt/data }
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: hermes-my-agent
```

Apply the Job, wait for completion, then `kubectl apply` the HermesAgent CR. The agent boots against your pre-seeded config.

This is the cleanest pattern if you build agents from a template: you control `config.yaml` upfront without having to `kubectl exec` later.

## Option C: use a base image with a sanitized default

If you control the agent image (or you can switch to the UndermountainCC `hermes` base image), bake the right `model.default` into the image's `cli-config.yaml.example`. The agent's first-boot copy then lands the value you want without any post-boot patching.

Use this for organisations running many agents with the same default provider.

## Why `spec.llmDefaultProvider` still exists

The field stamps `HERMES_INFERENCE_PROVIDER` on the container env, and the CRD validates it matches an entry in `spec.llmProviders[].name`. A misspelled provider name is rejected at the API server, not at runtime. It also reflects intent in the spec, which is useful for GitOps audit trails even if it does not drive the runtime.

`HERMES_INFERENCE_MODEL` does not help: upstream consults it only in `hermes -z` oneshot mode, not in `hermes gateway run`. Do not bother stamping it.

## See also

- [Known Hermes upstream behaviours](../concepts/upstream-behaviours.md) — the `model.default` gotcha in context.
- [PVC sovereignty](../concepts/storage.md) — why the operator does not auto-patch `config.yaml` for you.
- [Reference: Troubleshooting catalogue](../reference/troubleshooting.md) — the DeepSeek HTTP 400 symptom.
