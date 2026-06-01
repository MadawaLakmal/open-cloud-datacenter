# ADR-0002: Vendor the Harbor chart into the operator image

**Status:** Accepted
**Date:** 2026-06-01

## Context

ADR-0001 commits us to the Helm Go SDK for Harbor installation. The next
question: where does Helm get the chart tarball from at reconcile time?

Two options:

| Option | Behaviour |
|---|---|
| Runtime download | Operator calls `helm.goharbor.io` per reconcile. Same chart, possibly different bits depending on when the request lands. |
| Vendored | Chart tarball is committed under `charts/` and copied into the operator's container image. Reconciles read from disk. |

Local testing on Apple Silicon also surfaced that distroless containers
don't have a writable home dir. Helm's runtime download path lands files
in `$HOME/.cache/helm` which doesn't exist; we hit it before and worked
around it with env-var redirection. Removing the download removes the
class of bug.

## Decision

Vendor the Harbor chart into the repo under `registry/charts/<version>.tgz`.
The Dockerfile copies the directory into the runtime image at `/charts/`.
`helmrunner.Runner.Ensure()` resolves the chart with
`loader.LoadFile("/charts/harbor-<version>.tgz")`.

A SHA-256 in `charts/CHECKSUMS.txt` pins every tarball. `make verify-chart`
checks it; CI runs `verify-chart` on every PR.

`make sync-chart HARBOR_VERSION=X.Y.Z` is the operator-team-only command
that downloads a new tarball and appends a CHECKSUMS entry.

## Consequences

- **Reproducible installs.** Same operator image → same chart contents.
  No "the URL changed" or "the mirror was rebuilt" surprises.
- **No runtime network dependency** on Harbor's chart repo. Important for
  air-gapped Harvester clusters.
- **Auditable.** Every chart bump is a PR with a SHA-256 diff that
  reviewers can verify by running `make sync-chart` themselves.
- **Image size grows slightly.** The harbor-1.14.0 tarball is ~50KB;
  trivial.
- **Bump procedure becomes deliberate.** Cannot accidentally adopt a new
  upstream chart by waiting; someone has to run `make sync-chart` and
  open a PR. See `docs/chart-management.md` for the PR review gate.
- **Multiple versions can coexist** during a deprecation window. If a
  tenant pinned `spec.harborVersion: 1.14.0` and the operator's default
  has moved to `1.15.0`, the older tarball stays in `charts/` for one
  minor release.

## When to revisit

Revert to runtime download if:

- The committed tarballs grow so large the operator image becomes
  unwieldy (unlikely — chart YAML is tiny).
- We need to support arbitrary user-supplied chart versions (out of scope
  for v0.1; tenants don't pick chart versions — dc-api / operator team
  does).

Until then, vendoring is the production-grade default.
