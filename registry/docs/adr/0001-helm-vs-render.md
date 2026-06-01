# ADR-0001: Use Helm SDK for Harbor (not in-Go rendering)

**Status:** Accepted
**Date:** 2026-06-01

## Context

Two precedents on the `operators` branch:

- `keyvault/` renders OpenBao's StatefulSet, Services, ConfigMaps, RBAC
  by hand in Go (`internal/controller/render/`). No Helm.
- `database/` provisions per-tenant VMs via the Harvester API. Also no
  Helm.

If we follow either pattern for the registry operator, we'd reimplement
Harbor's installation in Go.

Harbor is significantly more complex than OpenBao or a single VM. It has
**10+ tightly-coupled components**:

- harbor-core (Deployment, ~50 env vars, mounts a ConfigMap with ~300
  keys)
- harbor-portal, harbor-jobservice, harbor-nginx, harbor-registry
- harbor-database (StatefulSet, postgres), harbor-redis (StatefulSet)
- harbor-trivy (StatefulSet, opt-in)
- Inter-component Services, Secrets, ConfigMaps, PVCs, ServiceAccounts
- An internal TLS chain between components when enabled
- A migration Job for postgres schema bumps
- Optional Ingress / NetworkPolicy

The Harbor team maintains a Helm chart that does this work. Their chart is
the de-facto installation surface for Harbor and tracks upstream changes
release-for-release.

## Decision

The registry operator uses the **Helm Go SDK** to install, upgrade, and
uninstall the Harbor chart inside tenant namespaces.

The chart itself is **vendored into the operator image** at
`/charts/harbor-<version>.tgz`. The operator never downloads from
`helm.goharbor.io` at reconcile time. See ADR-0002 for the vendoring
mechanics.

## Consequences

- One Go dependency on `helm.sh/helm/v3`.
- The operator carries the chart tarball inside its container image
  (~50KB for harbor-1.14.0). The Dockerfile copies `charts/` into the
  runtime layer.
- The reconciler's `helm install` call blocks for up to 10 minutes
  (default `Timeout`). `MaxConcurrentReconciles` defaults to 1, so the
  Backend reconciler can be busy with one tenant's install while another
  tenant's Backend waits. We bump concurrency once we have multi-tenant
  scale in dev.
- We track Harbor versions explicitly. Each operator release pins one
  default `spec.harborVersion`. Bumping is a reviewed PR — see
  `docs/chart-management.md`.
- We diverge from the upstream KeyVault/DBaaS pattern. The divergence is
  visible in `internal/helmrunner/` (which keyvault doesn't have) and
  `charts/` (same).

## When to revisit

Switch to in-Go rendering if any of the following becomes true:

- Harbor's chart stops being maintained or shifts to a model we cannot
  vendor.
- We need fine-grained per-CR overrides that the chart's values schema
  cannot express.
- The Helm SDK introduces a security or stability issue we can't tolerate
  (e.g. a CVE that requires moving off `helm.sh/helm/v3` for months).

Otherwise the cost of switching far exceeds the cost of staying.
