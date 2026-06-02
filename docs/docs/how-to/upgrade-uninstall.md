# Upgrade and uninstall

This guide shows you how to upgrade the operator to a newer release and how to fully uninstall it, including leftover resources from older versions.

## Upgrade

### Helm

```bash
helm upgrade hermes-operator oci://ghcr.io/undermountaincc/charts/hermes-operator \
    --namespace hermes-operator-system
```

CRDs are not auto-upgraded by `helm upgrade` (intentional, to avoid silently breaking stored CRs). When the CRD changes between operator versions, apply the new CRD manually:

```bash
kubectl apply -f https://raw.githubusercontent.com/UndermountainCC/hermes-operator/<new-tag>/config/crd/bases/hermes.k8s.undermountain.cc_hermesagents.yaml
```

### kustomize

```bash
kubectl apply -k https://github.com/UndermountainCC/hermes-operator//config/default?ref=<new-tag>
```

Apply the new CRD as above if it changed.

### Upgrading from a release that depended on cert-manager

Older releases shipped a validating admission webhook served via cert-manager TLS. The webhook is gone; cert-manager is no longer required. After upgrade, the cert-manager-generated Secrets are orphaned but still occupy a slot in the operator namespace. Clean them up:

```bash
kubectl -n hermes-operator-system delete secret webhook-server-cert metrics-server-cert --ignore-not-found
```

Safe to run unconditionally; `--ignore-not-found` is a no-op on fresh installs.

You can also remove cert-manager if no other workload depends on it, but that is a separate decision.

## Uninstall

Order matters: delete CRs first so the operator gets to run finalizers, then uninstall the operator, then delete the CRD.

```bash
kubectl delete hermesagents --all -A
helm uninstall hermes-operator --namespace hermes-operator-system
kubectl delete crd hermesagents.hermes.k8s.undermountain.cc
```

### PVCs survive by default

`spec.storage.retainPolicy` defaults to `Retain`. CRs you delete leave their PVCs behind. List what's left:

```bash
kubectl get pvc -A -l app.kubernetes.io/managed-by=hermes-operator
```

Delete the ones you no longer need:

```bash
kubectl -n <ns> delete pvc <pvc-name>
```

If you want CR-delete to cascade to the PVC, set `spec.storage.retainPolicy: Delete` on the CR before deleting it (see [PVC sovereignty](../concepts/storage.md)).

### Leftover Secrets and namespaces

`helm uninstall` removes resources the chart created, but anything you `kubectl create` separately (the operator namespace if you created it by hand, agent credential Secrets, and so on) is yours to clean up:

```bash
kubectl delete namespace hermes-operator-system  # if you no longer need it
kubectl delete namespace hermes                  # if you created one for agents
```

## Verify the uninstall

```bash
kubectl get crd | grep hermes
# (no output)

kubectl api-resources --api-group=hermes.k8s.undermountain.cc
# (no output)

kubectl get pods -A | grep hermes
# (any remaining: agent pods you didn't delete first, or unrelated workloads)
```

## See also

- [Install the operator](install.md) — install paths.
- [PVC sovereignty](../concepts/storage.md) — what happens to data on CR delete.
- [Reference: Troubleshooting catalogue](../reference/troubleshooting.md) — issues that can surface during upgrade.
