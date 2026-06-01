# Rollback

What to do when a Backend or Instance is stuck or behaving unexpectedly.

## Symptom matrix

| Symptom | Likely cause | First action |
|---|---|---|
| `RegistryBackend.status.phase=Failed`, message references "helm" | Helm install failed (RBAC, chart values, resource limits) | `kubectl describe rb` + `kubectl logs -n registry-system -l control-plane=controller-manager` |
| Backend stuck `Provisioning` for > 15 min | Harbor pod stuck Pending (storage, scheduling) or CrashLoopBackOff | `kubectl get pods -n dc-tenant-<slug>` then `kubectl describe pod` of the unhealthy one |
| Helm release state `pending-install` or `pending-upgrade` | Previous reconcile crashed mid-operation | "Helm state cleanup" below |
| `RegistryInstance.status.phase=Failed`, message "HarborProjectFailed" | Harbor API call failed (admin password wrong, network blocked) | `kubectl logs` for the API error message, then check the admin Secret |
| Backend / Instance stuck `Terminating` | A finalizer's cleanup is failing | "Stuck-deleting CRs" below |

## Helm state cleanup

If `helm list -n dc-tenant-<slug>` shows a release in
`pending-install`, `pending-upgrade`, or `pending-rollback`:

```bash
# Roll back to the last successful revision (if any)
helm rollback harbor 0 -n dc-tenant-<slug>
# Or if there's no successful revision, uninstall and let the operator retry
helm uninstall harbor -n dc-tenant-<slug>
```

Then patch the Backend to trigger a re-reconcile:

```bash
kubectl patch rb harbor -n dc-tenant-<slug> --type=merge -p '{"metadata":{"annotations":{"force-reconcile":"'"$(date +%s)"'"}}}'
```

## Stuck-deleting CRs

A CR in `Terminating` for > 5 min means the finalizer can't complete.
Diagnose first:

```bash
kubectl describe rb harbor -n dc-tenant-acme | grep -A2 Finalizers
kubectl logs -n registry-system -l control-plane=controller-manager --tail=50
```

Common causes:

- `helm uninstall` failed (RBAC issue, or Kubernetes API briefly
  unavailable). Retry by patching the CR (any patch will trigger a
  reconcile).
- Harbor's API is unreachable when deleting an `RegistryInstance`
  (RBAC, network policy, Harbor pod down). Bring Harbor back, the next
  reconcile completes the delete.

**Force removal — last resort.** Editing the CR to drop its finalizer
will free Kubernetes' lock but leak the underlying Helm release + Harbor
project:

```bash
kubectl patch rb harbor -n dc-tenant-acme \
  --type=json -p='[{"op":"remove","path":"/metadata/finalizers"}]'
# Now you OWN the cleanup of:
#   helm uninstall harbor -n dc-tenant-acme
#   the dc-tenant-acme namespace and its PVCs
```

For `RegistryInstance`:

```bash
kubectl patch ri billing-registry -n dc-acme-billing \
  --type=json -p='[{"op":"remove","path":"/metadata/finalizers"}]'
# Now you OWN:
#   DELETE /api/v2.0/projects/<id>/robots/<id>
#   DELETE /api/v2.0/projects/<id>
# against the tenant's Harbor API, by hand.
```

Only do this in dev or with explicit ack from the on-call.

## Reverting a chart bump

See `docs/chart-management.md` "Rollback" section. The short version:

```bash
git revert <chart-bump-PR>
make docker-build docker-push IMG=ghcr.io/.../registry-operator:<rollback-tag>
make deploy       IMG=ghcr.io/.../registry-operator:<rollback-tag>
```

This restores the operator's previous default chart. Existing Backends
that already migrated to the new chart stay on the new chart until you
explicitly patch each `spec.harborVersion` back — the operator does NOT
downgrade automatically (downgrading Harbor across schema bumps is
unsafe).

## When all else fails

- Save logs: `kubectl logs -n registry-system -l control-plane=controller-manager --previous > operator.log`
- Save CR state: `kubectl get rb,ri -A -o yaml > cr-snapshot.yaml`
- Save events: `kubectl get events -A --sort-by=.lastTimestamp > events.txt`
- File an issue with that bundle.
