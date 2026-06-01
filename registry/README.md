# Registry Operator

Kubernetes operator that manages **Harbor** registries inside the WSO2 Open
Cloud Datacenter. Plugs into the dc-api control plane via two CRDs under
`registry.opencloud.wso2.com/v1alpha1`.

## What it does

Watches two custom resources:

| Kind | Lives in | Represents |
|---|---|---|
| `RegistryBackend` | `dc-tenant-<slug>` | One Harbor cluster for a tenant. Operator installs Harbor via a **vendored Helm chart** (`charts/harbor-<version>.tgz`). |
| `RegistryInstance` | `dc-<tenant>-<project>` | One Harbor-project inside that tenant's Harbor. Operator calls the Harbor API to create the project + a robot account, ships credentials in a Secret. |

dc-api creates the CRs in response to user requests; this operator turns
them into actual Harbor deployments and Harbor-projects.

The vendored Harbor chart is the source of truth for what gets installed.
See [docs/chart-management.md](docs/chart-management.md) for how chart
versions are pinned, bumped, and reviewed.

## Layout

```
api/v1alpha1/                       CRD Go types (RegistryBackend, RegistryInstance)
cmd/main.go                         Manager entrypoint (kubebuilder shape)
internal/controller/                Reconcilers + label/finalizer helpers
internal/helmrunner/                Helm Go SDK wrapper — loads vendored tarball
internal/harbor/                    Harbor REST client (projects, robots, retention)
charts/                             Vendored Harbor chart tarball(s) + checksums + baseline values
config/                             kubebuilder kustomize tree (CRDs, manager, RBAC, samples)
docs/                               Architecture diagrams + ADRs + operator runbooks
Dockerfile, Makefile, PROJECT       Standard kubebuilder build files
```

## Quickstart

```bash
# Run tests (envtest)
make test

# Build the image for your platform
make docker-build IMG=ghcr.io/<you>/registry-operator:dev

# Push (multi-arch)
make docker-buildx IMG=ghcr.io/<you>/registry-operator:dev

# Deploy to the cluster pointed at by ~/.kube/config
make deploy IMG=ghcr.io/<you>/registry-operator:dev

# Apply sample CRs (after creating the namespaces they target)
kubectl create namespace dc-tenant-acme
kubectl apply -f config/samples/registry_v1alpha1_registrybackend.yaml
kubectl create namespace dc-acme-billing
kubectl apply -f config/samples/registry_v1alpha1_registryinstance.yaml
```

See [docs/install.md](docs/install.md) for the full install runbook,
[docs/upgrade.md](docs/upgrade.md) for chart version bumps, and
[docs/test-environments.md](docs/test-environments.md) for the Harvester
dev environment recipe.

## Why Helm and not in-Go rendering

Unlike KeyVault (OpenBao = one StatefulSet) and DBaaS (per-tenant VMs),
Harbor is **10+ tightly-coupled components**. Reimplementing the Harbor
chart in Go would be thousands of lines that we'd have to keep aligned
with Harbor's upstream releases forever. We use the Helm SDK + a vendored
chart instead. [ADR-0001](docs/adr/0001-helm-vs-render.md) records the
decision and the trigger that would make us revisit it.

## Status

Pre-`operators/v0.1.0`. SemVer §4 — anything in `0.x` MAY change.
