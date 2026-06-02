# hermes-operator

Kubernetes operator for [Hermes](https://github.com/NousResearch/hermes-agent), a stateful AI agent that talks to humans through messaging platforms and calls LLM providers.

## What it does

Define an agent declaratively:

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
  llmDefaultProvider: anthropic
  llmProviders:
    - name: anthropic
      env:
        - name: ANTHROPIC_API_KEY
          valueFrom:
            secretKeyRef: { name: creds, key: ANTHROPIC_API_KEY }
  gateways:
    - type: discord
      env:
        - name: DISCORD_BOT_TOKEN
          valueFrom:
            secretKeyRef: { name: creds, key: DISCORD_BOT_TOKEN }
```

`kubectl apply` and the operator creates Deployment, PVC, ServiceAccount, Service, optional RBAC bindings, and composes env vars from the spec layers. Status reports per-gateway connection state.

## Where to start

- **New here?** [Quickstart (10 minutes)](tutorials/quickstart.md) gets you from zero to a running agent.
- **Want a real messaging gateway?** [Your first HermesAgent (Discord)](tutorials/your-first-hermesagent.md) walks through a Discord-backed agent end to end.
- **Installing on a real cluster?** [Install the operator](how-to/install.md) covers Helm and raw kustomize.
- **Looking up a CR field?** [Reference: HermesAgent API](reference/api-reference.md) documents every field.

## How the docs are organised

- **Tutorials**: step-by-step learning. Start here if it's your first day.
- **How-to guides**: recipes for specific operational tasks (install, expose dashboard, change LLM provider, troubleshoot).
- **Concepts**: explanations of how the system works (architecture, PVC sovereignty, RBAC, lifecycle, upstream behaviours).
- **Reference**: exhaustive field, flag, metric, and Helm value documentation.
- **Project**: what's new, contributing, license.

## Status

v1alpha1. The CRD's `apiVersion` will change at each alpha/beta/stable transition. Track [CHANGELOG.md](https://github.com/UndermountainCC/hermes-operator/blob/main/CHANGELOG.md).

## Read more

Background reading on the [Undermountain](https://undermountain.cc/) engineering blog:

- [hermes-operator: Kubernetes-Native AI Agents Without the YAML Sprawl](https://undermountain.cc/blog/hermes-operator-kubernetes-native-ai-agents) — an overview of hermes-operator and what it gives you over hand-rolled manifests.
- [Per-Session Pods: How hermes-operator Sandboxes Shell-Calling Agents](https://undermountain.cc/blog/kubernetes-exec-backend-isolated-session-pods) — a deeper dive on the kubernetes exec backend and the VAP hardening model.
