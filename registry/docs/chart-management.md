# Harbor Chart Management

How we keep the vendored Harbor chart current, secure, and reviewed.
This document is the operator team's production-grade process for chart
bumps, rollbacks, and security incidents.

## Vocabulary

- **Chart version** — the Harbor Helm chart's own version (e.g. `1.14.0`).
  This is what `RegistryBackend.spec.harborVersion` and
  `charts/harbor-<version>.tgz` refer to. Each chart version pins a
  specific Harbor app version internally.
- **Harbor app version** — the user-visible Harbor release (e.g. `v2.10.0`).
  Read from `Chart.yaml` inside the tarball. The operator does NOT use
  this directly; the chart version is the contract.

The mapping between the two is in Harbor's [chart release
notes](https://github.com/goharbor/harbor-helm/releases).

## Pinning policy

The operator pins **one default** chart version. That's the version a
freshly-created `RegistryBackend` gets when its spec omits
`harborVersion`. Tenants can pin an older version explicitly as long as
its tarball is still committed under `charts/`.

We support **at most two versions side-by-side** at any time:

- The current default
- The previous default (during a one-minor deprecation window)

Older tarballs are deleted after the window closes.

## Bumping the chart

Codified end-to-end as `make sync-chart HARBOR_VERSION=X.Y.Z`. The full
process:

### 1. Run the sync target

```bash
cd registry
make sync-chart HARBOR_VERSION=1.15.0
```

This:
- Downloads `https://helm.goharbor.io/harbor-1.15.0.tgz` into `charts/`.
- Computes its SHA-256 and appends to `charts/CHECKSUMS.txt`.
- Prints next-step instructions.

### 2. Read Harbor's release notes

Open the [harbor-helm release page](https://github.com/goharbor/harbor-helm/releases)
and read every entry between the previous version and the new one. Pay
attention to:

- **Required spec changes** (a new mandatory value in `values.yaml`)
- **Removed values** (we may still pass them in our buildHelmValues —
  silently ignored but worth a comment)
- **Sub-component version bumps** (postgres major, redis major, trivy)
- **Breaking changes** flagged in the changelog

### 3. Diff values

```bash
helm show values charts/harbor-1.15.0.tgz | diff -u charts/values-baseline.yaml -
```

Reconcile any drift between our `values-baseline.yaml` and the chart's
defaults. If the chart added a new top-level field we want to default
explicitly, add it to `values-baseline.yaml`.

### 4. Update the CRD default

In `api/v1alpha1/registrybackend_types.go`:

```go
// +kubebuilder:default="1.15.0"
HarborVersion string `json:"harborVersion,omitempty"`
```

Then regenerate: `make manifests generate`.

### 5. Remove the previous tarball (if leaving the window)

If you're inside the one-minor deprecation window, keep the previous
tarball. Otherwise:

```bash
git rm charts/harbor-<old-version>.tgz
# Edit charts/CHECKSUMS.txt to remove the line for the old version.
```

### 6. Smoke test locally and on harvester-dev

- `make test` — envtest unit tests
- `make verify-chart` — checksum verification
- On `harvester-dev`: roll the new operator image, watch an existing
  `RegistryBackend` cycle through phase=Provisioning → Ready under the
  new chart. Confirm Harbor's `/api/v2.0/health` returns healthy.
- Push a tag via `crane copy` to confirm robots still work.

### 7. Open a PR

Title: `chart: bump Harbor to 1.15.0 (Harbor 2.11.x app)`

PR body must contain (the CI workflow does not enforce this; reviewers
do):

- Link to the Harbor release notes
- The values diff from step 3
- The result of step 6 (paste `kubectl get rb -A` and a successful crane
  push line)
- Tenant-visible behaviour changes, if any

### 8. Reviewer checklist

Before approving:

- [ ] Release notes read; no unmanaged breaking changes
- [ ] `charts/CHECKSUMS.txt` line matches the upstream tarball
  (independently downloaded by the reviewer and `shasum`'d)
- [ ] `make verify-chart` passes locally
- [ ] `make test` passes in CI
- [ ] harvester-dev smoke result attached
- [ ] Rollback path tested — see "Rollback" below

## Rollback

If a bump misbehaves in production:

1. **Operator-level rollback.** Revert the bump PR. CI publishes a new
   operator image with the old default chart pinned. New `RegistryBackend`
   CRs go back to the old version.
2. **Tenant-level rollback.** For an existing Backend that's stuck on the
   new version: patch its CR to set `spec.harborVersion: <old>`. The
   reconciler runs `helm upgrade` back to the old tarball (which must
   still be committed, hence the deprecation window).
3. **Helm-level rollback (last resort).** Per-tenant:
   `helm rollback harbor <revision> -n dc-tenant-<slug>`. This is
   destructive to the operator's state tracking — only use when a
   `RegistryBackend` is wedged and CR-level rollback isn't taking effect.

## Security scanning

CI runs `trivy fs charts/` on every PR. Findings are blocking. Possible
findings:

- **Embedded image tag with a known CVE.** The Harbor chart references
  specific sub-component image tags (postgres, redis, trivy itself). If
  trivy flags one, check whether Harbor has published a chart patch
  release; if not, raise it with the Harbor team and either pin an
  override via `values-baseline.yaml` or accept the finding with a
  documented justification.
- **Hard-coded secret in chart YAML.** Vanishingly rare. Treat as a
  Harbor upstream bug.

## Multiple versions side-by-side

The reconciler resolves the chart path at request time:

```go
chartPath := filepath.Join("/charts", "harbor-"+rb.Spec.HarborVersion+".tgz")
```

So as long as `charts/harbor-X.Y.Z.tgz` exists in the image, that Backend
gets that version. Useful during a deprecation window — new Backends get
the new default, existing ones with pinned versions keep their old
tarball.

## Pre-flight checklist before publishing a release tag

When tagging `operators/vX.Y.Z`:

- [ ] One and only one default chart version is set in
  `registrybackend_types.go`.
- [ ] All tarballs in `charts/` have entries in `CHECKSUMS.txt`.
- [ ] No orphaned tarballs (committed but not referenced by either the
  default or an in-window deprecation).
- [ ] `docs/chart-management.md` "Pinning policy" matches reality.
