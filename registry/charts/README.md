# Vendored Harbor chart

This directory holds the Harbor Helm chart tarballs the registry operator
installs into tenant namespaces. The operator never downloads from
`helm.goharbor.io` at reconcile time — it loads the tarball directly via
`helm.sh/helm/v3/pkg/chart/loader.LoadFile()`.

Vendoring the chart gives us:

- **Reproducible installs.** Same operator image → same chart contents.
- **No runtime network dependency** on Harbor's chart repo.
- **Audit trail.** Every chart bump is a reviewed PR with a SHA256 diff.
- **Air-gapped support.** Operators in disconnected Harvester clusters work
  without an upstream proxy.

## Files

| File | Purpose |
|---|---|
| `harbor-<version>.tgz` | The Harbor chart tarball, exactly as published. |
| `CHECKSUMS.txt` | SHA-256 of each tarball. Verified by `make verify-chart`. |
| `values-baseline.yaml` | Default values overlay the operator merges before the user's overrides. |

## Bumping the Harbor version

```bash
make sync-chart HARBOR_VERSION=1.15.0
```

The target downloads the tarball, appends a new line to `CHECKSUMS.txt`,
saves the file under `charts/`. After that:

1. Read [Harbor's release notes](https://github.com/goharbor/harbor/releases)
   for breaking changes.
2. Diff values:
   `helm show values charts/harbor-1.15.0.tgz | diff -u charts/values-baseline.yaml -`
3. Remove the old `charts/harbor-<old>.tgz` and its line from `CHECKSUMS.txt`
   (or keep both during a deprecation window; see "Multiple versions" below).
4. Update the default in `api/v1alpha1/registrybackend_types.go` (the
   `+kubebuilder:default="1.14.0"` marker on `HarborVersion`).
5. Run `make manifests generate test` and the harvester-dev smoke test.
6. Open a PR.

See `docs/chart-management.md` for the full PR review gate.

## Multiple versions side-by-side

The operator can serve any chart version whose tarball is present in this
directory (and listed in `CHECKSUMS.txt`). Keeping older tarballs in for one
release lets existing `RegistryBackend` CRs keep their pinned version while
new Backends use the new default. We commit to a **one-minor deprecation
window** before deleting an in-use chart.

## Security scanning

CI runs `trivy fs charts/` on every PR. Vulnerabilities in chart contents
(rare — these are YAML, not binaries) block merge until reviewed.
