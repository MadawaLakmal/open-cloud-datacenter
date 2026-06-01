# Open Cloud Datacenter — Operators

> **You're on the `operators` branch of `open-cloud-datacenter`.**
> This branch holds the source code for Kubernetes operators that the
> DC-API control plane (on the [`controlplane`
> branch](https://github.com/wso2/open-cloud-datacenter/tree/controlplane))
> dispatches custom resources to. Each operator is a self-contained
> kubebuilder project under a top-level directory.
>
> Deployment of these operators is handled by the [`terraform`
> branch](https://github.com/wso2/open-cloud-datacenter/tree/terraform)
> under `modules/operators/<name>/`. This branch is *just* the source.

## Operators in this branch

| Operator | Directory | Status | API group |
|---|---|---|---|
| Key Vault | [`keyvault/`](./keyvault/) | v0.0.x | `keyvault.opencloud.wso2.com` |
| Database | [`database/`](./database/) | v0.0.x | `dbaas.opencloud.wso2.com` |
| Registry | [`registry/`](./registry/) | v0.0.x | `registry.opencloud.wso2.com` |

## Layout convention

Each operator is a complete kubebuilder project root — no shared root
`go.mod` or `Makefile`. Walk into the operator's directory and use its
own `make` targets:

```bash
cd keyvault
make manifests   # regen CRDs from Go types
make generate    # regen zz_generated_*.go
make test        # envtest-backed unit tests
make docker-build IMG=ghcr.io/<your-org>/keyvault-operator:dev
```

Inside an operator directory:

```
<operator>/
├── api/            CRD Go types + zz_generated DeepCopy
├── cmd/            main.go (controller-runtime entrypoint)
├── config/         kustomize tree (CRDs, RBAC, manager Deployment, …)
├── internal/       reconciler logic
├── test/           e2e + integration tests
├── go.mod
├── Dockerfile      multi-stage builder + distroless
├── Makefile        kubebuilder-standard targets
├── PROJECT         kubebuilder project metadata
└── README.md       per-operator usage
```

## How operators get deployed

This branch ships *source only*. Deployment to a cluster is the job of
the [`terraform`](https://github.com/wso2/open-cloud-datacenter/tree/terraform)
branch's `modules/operators/<name>/` module, which renders the
`config/default/` kustomize tree into typed Terraform resources +
points the manager `Deployment` at a published image tag.

The published images live at whatever GHCR org / registry the consumer
publishes them to. The kubebuilder `Dockerfile`s here are the source
for building those images.

## Versioning

Per [OCDC branch model](https://github.com/wso2/open-cloud-datacenter#branch-model):
collective tagging under `operators/vX.Y.Z`. Per-operator tagging
(`operators/keyvault/vX.Y.Z`) is reserved for if the bundled-release
cadence becomes awkward — start collective.

Current: pre-`operators/v0.1.0`; SemVer §4 says anything in `0.x` MAY
change.
