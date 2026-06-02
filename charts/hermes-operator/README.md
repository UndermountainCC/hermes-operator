# hermes-operator Helm chart

Installs the [hermes-operator](https://github.com/UndermountainCC/hermes-operator) into a Kubernetes cluster.

## Install

```bash
helm repo add hermes-operator oci://ghcr.io/undermountaincc/charts
helm install hermes-operator hermes-operator/hermes-operator \
    --namespace hermes-operator-system --create-namespace
```

## Values

See [values.yaml](./values.yaml) for the full reference. Common overrides:

Recommended for multi-value strings (Helm's `--set` parses commas as list separators):

```yaml
# my-values.yaml
operator:
  allowedClusterRoles: "cluster-admin,admin"
image:
  tag: v0.1.2
```

```bash
helm upgrade hermes-operator hermes-operator/hermes-operator \
    --namespace hermes-operator-system \
    -f my-values.yaml
```

Or via `--set` with an escaped comma for the inline case:

```bash
helm upgrade hermes-operator hermes-operator/hermes-operator \
    --namespace hermes-operator-system \
    --set "operator.allowedClusterRoles=cluster-admin\,admin" \
    --set image.tag=v0.1.2
```

## Upgrade

```bash
helm upgrade hermes-operator hermes-operator/hermes-operator \
    --namespace hermes-operator-system
```

## Uninstall

```bash
# Delete all HermesAgent CRs first to allow finalizer cleanup.
kubectl delete hermesagents --all -A
helm uninstall hermes-operator --namespace hermes-operator-system

# CRDs are NOT removed by helm uninstall (intentional — to prevent data loss).
# Delete them explicitly if desired:
kubectl delete crd hermesagents.hermes.k8s.undermountain.cc
```
