# PVC sovereignty

This page explains the contract between the operator and `$HERMES_HOME` (the agent's persistent home directory at `/opt/data`), and what it means for you in day-to-day operation.

## What lives on the PVC

The PVC the operator creates for each agent is mounted at `/opt/data`. After the agent boots, it contains:

- `config.yaml`: runtime config (model defaults, gateway settings, drain timeouts).
- `sessions.db`: SQLite SessionDB recording every turn the agent has handled.
- `gateway.lock` + `gateway.pid`: the flock and pid file enforcing single-instance.
- Log files (`gateway.log`, `agent.log`, etc.).
- `skills/`: the SKILL.md tree the agent uses, agent-modifiable.
- Anything else the agent self-installs (dotfiles, `.local/bin/`, scratch).

## The contract

**The operator never writes under `$HERMES_HOME` after the PVC is created.** Not on first boot, not on reconcile, not on upgrade. The agent is the sole author of its own home directory.

In practice this rules out several things you might expect an operator to do:

- It will not bootstrap `config.yaml` from your CR spec.
- It will not copy skills onto the PVC.
- It will not clobber a file the agent has edited.

## What this means for you

- **To change runtime config, edit the file.** `kubectl exec` into the agent pod and edit `/opt/data/config.yaml`, then trigger a pod-level restart (`kubectl rollout restart deployment/hermes-<name>`) so the new process picks it up. There is no CR field that proxies `config.yaml` keys.
- **Sessions are real conversations.** Overwriting `sessions.db` would lose history; the operator treats it as inviolable.
- **Skill updates do not auto-roll.** If the image ships an updated `SKILL.md`, agents that have already booted keep their existing copy. Adoption is opt-in: delete the local copy and re-sync.

## Adopting a pre-existing PVC

If you have a PVC whose name does not follow the operator's `hermes-<name>-data` convention — for example, a legacy claim from a previous deployment — set `spec.storage.existingClaimName` instead of `spec.storage.persistentVolumeClaim`:

```yaml
spec:
  storage:
    existingClaimName: hermes-data   # pre-existing claim to mount
```

When `existingClaimName` is set:

- The operator mounts the named PVC verbatim at `/opt/data`. It does NOT create, reconcile, or set an ownerRef on that PVC.
- `retainPolicy` is ignored (the operator never owns a pre-existing PVC — you manage its lifecycle).
- The two fields are mutually exclusive: the CRD rejects a spec that sets both `existingClaimName` and a `persistentVolumeClaim` with access modes.

This field is additive and backward-compatible. Agents that do not set it behave exactly as before.

## Retention on CR delete

`spec.storage.retainPolicy` decides what happens to the PVC when you `kubectl delete hermesagent <name>`:

| Value | Effect |
|---|---|
| `Retain` *(default)* | PVC has no owner reference. CR delete leaves the PVC behind. You delete it yourself when ready. |
| `Delete` | PVC carries a controller ownerRef. CR delete cascades to the PVC via Kubernetes garbage collection. |

`Retain` is the safe choice for any agent whose state you care about. `Delete` is for ephemeral test agents you wire up in CI.

## PVC shape changes after creation

Many fields on `corev1.PersistentVolumeClaimSpec` (most notably `storageClassName`) are immutable post-creation by Kubernetes itself; the operator cannot change them by editing the CR. For storage classes that support volume expansion, you can grow the PVC manually with `kubectl edit pvc hermes-<name>` and the operator will not undo the change.

## Backup and DR

The operator does not manage snapshots, backups, or replication. Use your cluster's existing tooling:

- Velero with the CSI plugin for snapshot-based backup.
- StorageClass-level replication where the driver supports it (Rook-Ceph mirror, etc.).
- A periodic Job that mounts the PVC read-only and copies `$HERMES_HOME` out.

A snapshot taken mid-drain restores like a hard pod kill: the next boot calls `suspend_recently_active()` on every session that was active when the snapshot was taken.

## See also

- [Lifecycle, phases, and conditions](lifecycle.md) — termination grace, restart paths.
- [Known Hermes upstream behaviours](upstream-behaviours.md) — the `gateway.lock` flock contract.
- [Reference: HermesAgent API](../reference/api-reference.md#hermesagentstorage) — every storage field.
