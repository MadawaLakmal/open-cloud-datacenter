# Upgrade

Two distinct upgrade tracks:

1. **Operator** — bumping the manager pod's image to a newer release.
2. **Harbor** — bumping the chart version one or more tenants run.

They're independent. A new operator release MAY include a new default
Harbor chart, but a tenant can pin an older version per
[chart-management.md](chart-management.md).

## Upgrading the operator

```bash
cd registry
git pull
make docker-build IMG=ghcr.io/<your-user>/registry-operator:vX.Y.Z
make docker-push  IMG=ghcr.io/<your-user>/registry-operator:vX.Y.Z
make deploy       IMG=ghcr.io/<your-user>/registry-operator:vX.Y.Z
```

`make deploy` is idempotent. It runs `kustomize edit set image` and
`kubectl apply` — Kubernetes rolls the manager Deployment in place.

Verify:

```bash
kubectl rollout status deployment -n registry-system controller-manager --timeout=2m
kubectl logs -n registry-system -l control-plane=controller-manager --tail=20
```

The manager picks up where the previous instance left off — controller-
runtime reconciles all existing CRs on startup. Reconciles are
idempotent; no migration step is needed for in-flight CRs.

## Upgrading Harbor for a single tenant

Patch the `RegistryBackend`:

```bash
kubectl patch rb harbor -n dc-tenant-acme --type=merge \
  -p '{"spec":{"harborVersion":"1.15.0"}}'
```

The reconciler detects the change, calls `helm upgrade` with the
matching tarball from `/charts/`, blocks until pods are Ready (up to
10 min), then sets `status.phase=Ready` again.

Watch:

```bash
kubectl get rb -n dc-tenant-acme harbor -w
kubectl get pods -n dc-tenant-acme -w
```

If pods don't all come Ready within the timeout, the reconciler marks
the Backend `Failed`. Investigate (`kubectl describe pod`,
`kubectl logs`), fix, and patch the Backend again — same version is
fine, just to re-trigger.

## Upgrading Harbor for many tenants (operator-default bump)

Two-step process:

1. **Operator release.** New operator image, new default `harborVersion`
   in the CRD. Existing Backends keep their `spec.harborVersion` so they
   don't move yet.
2. **Tenant rollout.** dc-api (or an admin script) patches each Backend
   to the new version on a controlled schedule.

The previous chart tarball must stay in `charts/` during the rollout
window — that's the deprecation policy in
`docs/chart-management.md`.

## What changes during an upgrade

The chart's `helm upgrade` produces a Kubernetes rolling restart of each
Harbor Deployment:

- harbor-portal: ~10s
- harbor-jobservice: ~30s (graceful drain of in-flight jobs)
- harbor-registry: ~30s
- harbor-core: ~30s (the longest, since it serves API requests)
- StatefulSets (database, redis, trivy): in order, one pod at a time

Total elapsed: ~3-5 minutes for a no-schema-change upgrade.

A schema migration (postgres version bump, embedded in Harbor) adds a
one-shot Job: another 1-2 minutes. The chart blocks the upgrade until
the migration Job completes.

## When to NOT upgrade

- During a window where dc-api is mid-batch-creating Backends. Wait
  until those Backends reach Ready, otherwise the in-flight Helm
  install + the upgrade can both race against the same release.
- Without reading the Harbor release notes for the new version. Some
  bumps require new values; the chart fails clearly when one's missing,
  but better to know first.

## Rollback

See `docs/rollback.md` and `docs/chart-management.md`.
