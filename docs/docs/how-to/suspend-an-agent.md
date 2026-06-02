# Suspend an agent

Stop an agent without deleting its CR or PVC.

## When to use this

Use `spec.suspend` when you want to stop an agent declaratively and have GitOps reconciliation keep it stopped. Unlike `kubectl delete hermesagent`, suspending:

- Keeps the CR in etcd (no re-creation on next GitOps sync).
- Keeps the PVC and all agent state intact.
- Keeps the ServiceAccount, Service, and any RBAC bindings the operator created.

A common scenario: the agent is crash-looping and you want to stop it while you debug, without losing the CR or triggering a GitOps revert.

## Suspend

```bash
kubectl patch hermesagent <name> --type=merge -p '{"spec":{"suspend":true}}'
```

The operator sets `replicas: 0` on the Deployment and transitions `status.phase` to `Suspended`. `PodReady` becomes `False` with reason `Suspended` (never `Degraded`).

## Verify

```bash
kubectl get hermesagent <name>
# NAME    PHASE      IMAGE   AGE
# home    Suspended  ...     5d

kubectl get deployment hermes-<name> -o jsonpath='{.spec.replicas}'
# 0
```

## Unsuspend

```bash
kubectl patch hermesagent <name> --type=merge -p '{"spec":{"suspend":false}}'
```

The operator restores `replicas: 1` and the agent resumes from its PVC state as if after a normal pod restart.

## In a CR manifest

```yaml
spec:
  suspend: true   # add this line; remove or set false to resume
  image: ...
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 10Gi } }
```

## Notes

- The PVC is NOT touched while suspended. All agent state (`sessions.db`, `config.yaml`, etc.) is preserved.
- `retainPolicy` is unaffected. If you delete the CR while suspended, normal retention rules apply.
- `spec.suspend` works with both `storage.persistentVolumeClaim` and `storage.existingClaimName`.
