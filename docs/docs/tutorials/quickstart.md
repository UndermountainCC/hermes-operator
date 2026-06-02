# Quickstart (10 minutes)

In this tutorial, you will install the operator, create a minimal `HermesAgent`, and watch it reach `Ready`. Roughly ten minutes end-to-end.

## Prerequisites

- Kubernetes 1.29+.
- `kubectl` configured against the cluster.
- An LLM provider API key (e.g. DeepSeek, Anthropic).

## 1. Install the operator

```bash
helm install hermes-operator oci://ghcr.io/undermountaincc/charts/hermes-operator \
    --namespace hermes-operator-system --create-namespace
```

Verify the controller-manager came up:

```bash
kubectl -n hermes-operator-system rollout status deployment hermes-operator-controller-manager --timeout=120s
```

## 2. Create credentials

```bash
kubectl create namespace hermes
kubectl -n hermes create secret generic minimal-creds \
    --from-literal=DEEPSEEK_API_KEY=sk-...
```

## 3. Apply a HermesAgent

```yaml title="minimal.yaml"
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata:
  name: minimal
  namespace: hermes
spec:
  image: docker.io/nousresearch/hermes-agent:v2026.4.30
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 10Gi } }
  llmDefaultProvider: deepseek
  llmProviders:
    - name: deepseek
      env:
        - name: DEEPSEEK_API_KEY
          valueFrom:
            secretKeyRef: { name: minimal-creds, key: DEEPSEEK_API_KEY }
```

```bash
kubectl apply -f minimal.yaml
```

This is a complete, valid CR. No gateway is attached, so the agent will not be talkable-to from any messaging platform. That is covered in the next tutorial. For now, you are confirming the operator reconciles the CR end-to-end.

## 4. Watch reconciliation

```bash
kubectl -n hermes get hermesagent minimal -w
```

Expected progression:

```text
NAME      PHASE          IMAGE                                              AGE
minimal   Bootstrap      docker.io/nousresearch/hermes-agent:v2026.4.30     2s
minimal   Provisioning   docker.io/nousresearch/hermes-agent:v2026.4.30     8s
minimal   Ready          docker.io/nousresearch/hermes-agent:v2026.4.30     45s
```

If the phase sticks on `Bootstrap`, the operator is waiting for a `secretKeyRef` to resolve. Run `kubectl -n hermes describe hermesagent minimal` and look at the `SecretsResolved` condition.

## 5. Verify the agent is alive

```bash
kubectl -n hermes get pod -l app.kubernetes.io/name=hermes-agent
kubectl -n hermes logs deployment/hermes-minimal -c agent --tail=50
```

You should see `✓ Gateway is running`.

## Next steps

- [Your first HermesAgent (Discord)](your-first-hermesagent.md) — full field walkthrough with a real gateway attached.
- [Install the operator](../how-to/install.md) — production install paths and upgrade flow.
- [How a HermesAgent runs in your cluster](../concepts/architecture.md) — what the operator just created and why.
- [Reference: HermesAgent API](../reference/api-reference.md) — every spec field documented.

## Read more

- [hermes-operator: Kubernetes-Native AI Agents Without the YAML Sprawl](https://undermountain.cc/blog/hermes-operator-kubernetes-native-ai-agents) — background on what the operator gives you over hand-rolled manifests, on the Undermountain engineering blog.
