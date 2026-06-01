# Install

How to deploy the registry operator to a Kubernetes cluster from this
source. For routine production rollouts the [terraform branch](https://github.com/wso2/open-cloud-datacenter/tree/terraform)'s
`modules/operators/registry/` module is the canonical path; this doc is
for development environments and first installs.

## Prereqs

- `kubectl` pointed at your target cluster.
- `kustomize` and `controller-gen` available (the Makefile downloads them
  to `./bin/` on first use).
- Go 1.24+ for `make build` / `make test`. Not needed for `make deploy`.
- Docker + `docker buildx` for `make docker-build` and `make docker-buildx`.
- A container registry the cluster can pull from. Examples:
  `ghcr.io/<your-user>/registry-operator`,
  `docker.io/<your-user>/registry-operator`.

## 1. Build and push the operator image

```bash
cd registry
make docker-build IMG=ghcr.io/<your-user>/registry-operator:dev
make docker-push  IMG=ghcr.io/<your-user>/registry-operator:dev
```

For multi-arch (amd64 + arm64):

```bash
make docker-buildx IMG=ghcr.io/<your-user>/registry-operator:dev
```

## 2. Install CRDs

```bash
make install
```

Equivalent to: `kustomize build config/crd | kubectl apply -f -`.

## 3. Deploy the operator

```bash
make deploy IMG=ghcr.io/<your-user>/registry-operator:dev
```

Equivalent to:

```bash
cd config/manager && kustomize edit set image controller=<IMG>
kustomize build config/default | kubectl apply -f -
```

The Deployment lands in the namespace defined by
`config/default/kustomization.yaml`'s `namespace:` field (default
`registry-system`).

If the registry is private, create an image-pull Secret in the same
namespace and reference it via a kustomize patch on the manager
Deployment.

## 4. Verify

```bash
kubectl get pods -n registry-system
# expect: controller-manager-... 1/1 Running

kubectl logs -n registry-system -l control-plane=controller-manager --tail=20
# expect: "starting manager" and a healthy controller setup line per Kind

kubectl get crd | grep registry.opencloud.wso2.com
# expect:
#   registrybackends.registry.opencloud.wso2.com
#   registryinstances.registry.opencloud.wso2.com
```

## 5. Apply sample CRs

```bash
# Tenant namespace (one per tenant)
kubectl create namespace dc-tenant-acme
kubectl apply -f config/samples/registry_v1alpha1_registrybackend.yaml
kubectl get rb -n dc-tenant-acme -w
# Wait for PHASE=Ready (3-5 min on real Longhorn).

# Project namespace (one per dc-api project)
kubectl create namespace dc-acme-billing
kubectl apply -f config/samples/registry_v1alpha1_registryinstance.yaml
kubectl get ri -n dc-acme-billing -w
# Wait for PHASE=Ready (seconds — just two Harbor API calls).
```

## Uninstall

```bash
make undeploy        # removes the manager Deployment + RBAC
make uninstall       # removes the CRDs (also deletes every CR)
```

Removing CRDs cascades into every `RegistryBackend` and `RegistryInstance`
across the cluster, which triggers the finalizers (`helm uninstall`,
Harbor-project deletion). Do this only when you mean it.

See [docs/test-environments.md](test-environments.md) for environment-
specific recipes (harvester-dev, harvester-prod).
