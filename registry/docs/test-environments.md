# Test Environments

Three places we exercise the operator. Each has a kustomize overlay under
`config/envs/<env>/`. Pick the one matching your target and follow
`docs/install.md` with the env-specific image and namespace.

| Env | Where | When to use |
|---|---|---|
| **local** | kubebuilder envtest (in-process, via `make test`) | Unit + integration tests on every PR. Catches reconciler logic bugs against an in-memory API server. No Harbor install is exercised. |
| **harvester-dev** | A Harvester K8s cluster used for development | Realistic — full Helm install of Harbor, Longhorn PVCs, dc-api driving the CRs (if available). The first env where the chart actually runs. |
| **harvester-prod** | The production Harvester K8s cluster | The end state. No sample CRs applied by hand; dc-api creates the real ones. Two replicas, leader election on, pinned image tag. |

## Arm64 / Apple Silicon caveat

Harbor's official images (`goharbor/*` at any current minor) **only
publish linux/amd64 manifests**. Running them under QEMU on an Apple
Silicon Mac crashes with `runtime: lfstack.push invalid packing` —
this is a known Go-runtime issue with amd64 binaries under arm64
emulation, NOT a chart misconfig.

Implication: **don't try the full end-to-end test (operator + Harbor
install) on a Mac with k3s/kind/Rancher Desktop in arm64 mode.** Run
`make test` (envtest, native Go) locally to verify code; run the actual
Harbor install on Harvester (amd64).

## harvester-dev recipe

Prereqs:
- `kubectl` pointed at the dev cluster
- Longhorn installed; `kubectl get sc` shows `longhorn` (default or
  explicitly named)
- An image registry the cluster can pull from

Steps:

```bash
cd registry

# Build + push (multi-arch is fine; the cluster pulls amd64)
make docker-buildx IMG=ghcr.io/<your-user>/registry-operator:dev

# Deploy the operator
make deploy IMG=ghcr.io/<your-user>/registry-operator:dev

# Apply the sample Backend + Instance
kubectl create namespace dc-tenant-acme
kubectl apply -f config/samples/registry_v1alpha1_registrybackend.yaml
kubectl get rb -n dc-tenant-acme -w
# wait for PHASE=Ready

kubectl create namespace dc-acme-billing
kubectl apply -f config/samples/registry_v1alpha1_registryinstance.yaml
kubectl get ri -n dc-acme-billing -w
```

Smoke push:

```bash
SECRET=$(kubectl get secret -n dc-acme-billing -o name | grep creds)
USER=$(kubectl get $SECRET -n dc-acme-billing -o jsonpath='{.data.username}'  | base64 -d)
PASS=$(kubectl get $SECRET -n dc-acme-billing -o jsonpath='{.data.password}'  | base64 -d)
URL=$(kubectl  get $SECRET -n dc-acme-billing -o jsonpath='{.data.harbor_url}'| base64 -d)

kubectl run -i --rm --restart=Never --image=gcr.io/go-containerregistry/crane:debug crane-push -- \
  sh -c "
    crane auth login $URL --username $USER --password $PASS
    crane copy docker.io/library/alpine:latest $URL/billing/alpine:smoke
    crane ls $URL/billing/alpine
  "
```

If `smoke` is in the output, the full chain works.

## harvester-prod recipe

Install only. dc-api drives the CRs.

```bash
cd registry
make deploy IMG=ghcr.io/wso2/registry-operator:v0.1.0
kubectl rollout status deployment -n registry-system controller-manager --timeout=2m

# Confirm both CRDs are installed and the manager is watching
kubectl get crd | grep registry.opencloud.wso2.com
kubectl logs -n registry-system -l control-plane=controller-manager --tail=20 | grep "starting manager"
```

Then hand off to dc-api. Operator-side, monitor:

```bash
# All Backends across all tenants
kubectl get rb -A
# All Instances
kubectl get ri -A
# Recent reconciler errors
kubectl logs -n registry-system -l control-plane=controller-manager --tail=200 | grep -i error
```

Two replicas + leader election are configured in the prod overlay
(`config/envs/harvester-prod/`). One `coordination.k8s.io/lease` shows
up in `registry-system` named `registry-operator.opencloud.wso2.com`.

## What goes in each overlay

Each `config/envs/<env>/kustomization.yaml`:

- Sets the image tag (`images:` directive)
- Sets the namespace (defaults to `registry-system` for all envs)
- Optionally patches the manager Deployment for env-specific knobs
  (replicas, leader election, log level)

See `config/envs/harvester-dev/` for the concrete form.
