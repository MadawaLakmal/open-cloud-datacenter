# Security

What the registry operator hardens, what it does NOT defend against, and
where the trust boundaries are.

## Container image hardening

The operator image:

- Base: `gcr.io/distroless/static:nonroot` — no shell, no package manager.
- USER: `65532:65532` (the `nonroot` user the distroless image defines).
- Multi-arch (amd64 + arm64) via `make docker-buildx`.
- OCI labels for SBOM tooling (`org.opencontainers.image.{title,
  description, source, licenses}`).

## Pod security

The kustomize manager Deployment (`config/manager/manager.yaml`) sets:

| Field | Value |
|---|---|
| `securityContext.runAsNonRoot` | `true` |
| `securityContext.runAsUser` | `65532` (matches the image's USER) |
| `securityContext.runAsGroup` | `65532` |
| `securityContext.fsGroup` | `65532` |
| `securityContext.seccompProfile.type` | `RuntimeDefault` |
| `containers[].securityContext.readOnlyRootFilesystem` | `true` |
| `containers[].securityContext.allowPrivilegeEscalation` | `false` |
| `containers[].securityContext.capabilities.drop` | `["ALL"]` |

A writable `emptyDir` is mounted at `/tmp` (size limit 256 MiB) for the
Helm SDK's cache and the chart-extraction scratch space. This is the
operator's ONLY writable path inside the container.

This is the "restricted" Pod Security Standard. Clusters with PSA
admission set to `restricted` accept these pods without modification.

## RBAC

`config/rbac/role.yaml` defines a single `ClusterRole` the manager binds
to its `ServiceAccount`. Granted permissions:

- **Our CRDs** — full CRUD on `registrybackends`, `registryinstances` +
  their `/status` and `/finalizers` subresources.
- **Secrets** — full CRUD. We create the admin Secret per Backend and
  the robot-credentials Secret per Instance. Helm also stores its
  release state as Secrets.
- **Helm-installed resource types** — CRUD on ConfigMaps, Services,
  ServiceAccounts, PVCs, Pods, Endpoints, Events; CRUD on apps/Deployments,
  StatefulSets, DaemonSets, ReplicaSets; CRUD on batch/Jobs;
  CRUD on networking.k8s.io/Ingresses + NetworkPolicies; CRUD on
  policy/PodDisruptionBudgets; CRUD on rbac.k8s.io/Roles + RoleBindings.
- **Leader election** — `coordination.k8s.io/leases`.

The role is intentionally cluster-wide because the operator manages
resources in many tenant namespaces. Tightening to namespaced
`RoleBindings` per tenant is a future improvement once the namespace
list is bounded.

## Secret handling

### Admin Secret per Backend

- Name: `harbor-admin-credentials`
- Namespace: same as the `RegistryBackend` CR
- Owner reference: the `RegistryBackend` CR (garbage-collected with it)
- Data: `username=admin`, `password=<24-char random>`
- Labels: the seven `dc-api.wso2.com/*` labels propagated from the CR

The password is generated once on first sight of a new Backend
(`crypto/rand` → 18 bytes → 24-char URL-safe base64). The reconciler
re-reads the existing Secret on subsequent reconciles; rotation is NOT
in scope for v0.1.

### Robot credentials Secret per Instance

- Name: `registry-<UID>-creds`
- Namespace: same as the `RegistryInstance` CR
- Owner reference: the `RegistryInstance` CR
- Data: `username=robot$<name>`, `password=<one-time-from-harbor>`,
  `harbor_url=<in-cluster-svc-dns>`
- Labels: the seven `dc-api.wso2.com/*` labels propagated from the CR

Harbor reveals the robot's secret exactly once at create time. The
operator captures it then; rotation requires deleting the robot in Harbor
and reconciling the Instance.

## Finalizers

Both CRs carry per-Kind finalizers:

- `registry.opencloud.wso2.com/backend-cleanup` — added to every
  `RegistryBackend`. On delete, the reconciler runs `helm uninstall`
  before removing it.
- `registry.opencloud.wso2.com/instance-cleanup` — added to every
  `RegistryInstance`. On delete, the reconciler calls Harbor's
  `DELETE /api/v2.0/projects/<id>/robots/<id>` and
  `DELETE /api/v2.0/projects/<id>` before removing it.

If finalizer execution fails (helm uninstall hung, Harbor API down), the
CR remains in `Terminating` indefinitely. Operators with cluster-admin
can `kubectl patch` the CR to remove the finalizer manually — but the
underlying resources will then orphan and require manual cleanup.

## What this operator does NOT defend against

- **Compromise of dc-api.** dc-api is the authentication boundary. If
  dc-api is compromised, the attacker can create arbitrary Backends and
  Instances; the operator does what they say.
- **Cluster-admin compromise.** Anyone with cluster-admin can read every
  Secret in every namespace, including the Harbor admin password.
- **Harbor internal vulnerabilities.** The operator doesn't patch Harbor.
  Bumping the vendored chart is the only mechanism — see
  `docs/chart-management.md` and the trivy scan job in CI.
- **Network egress from Harbor.** Harbor pods can reach the cluster's
  network. Restricting Harbor's egress is a NetworkPolicy concern,
  applied by dc-api or a cluster-wide policy, not by this operator.

## Vulnerability reporting

Report security issues to the open-cloud-datacenter security contact
(see the repo root `CODE_OF_CONDUCT.md` or `SECURITY.md`, if present).
