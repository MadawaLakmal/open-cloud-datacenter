# Addons

Optional Harbor sub-components that tenants opt into. Each addon is one
boolean field on `RegistryBackend.spec.engineConfig.addons.<name>.enabled`.
dc-api surfaces these toggles as feature flags; an end user enables what
they need.

## Available addons

| Name | Field | Default | What it does |
|---|---|---|---|
| Trivy | `addons.trivy.enabled` | `false` | Container image vulnerability scanner. Adds the `harbor-trivy` StatefulSet + Service. Scans are triggered from Harbor's UI / API. |

That's the full v0.1 list. Future additions follow the same `AddonToggle`
shape — see "Adding a new addon" below.

## Enabling Trivy

dc-api patches the existing `RegistryBackend`:

```yaml
spec:
  engineConfig:
    addons:
      trivy:
        enabled: true
```

The reconciler runs `helm upgrade` with `trivy.enabled: true` in the
values. Helm reconciles the addition of the trivy StatefulSet + Service
+ PVC. The Backend phase cycles through `Provisioning` → `Ready` over
~2 minutes.

Disabling is the inverse: set `enabled: false`, the reconciler upgrades
to remove the trivy resources. Image scan history stored inside trivy's
PVC is lost; tenants are warned of this in the dc-api UI when toggling
off.

## Adding a new addon

Three places to touch:

### 1. CRD type

In `api/v1alpha1/registrybackend_types.go`:

```go
type BackendAddons struct {
    Trivy AddonToggle `json:"trivy,omitempty"`
    // Cosign verifies image signatures at admission.
    // +optional
    Cosign AddonToggle `json:"cosign,omitempty"`
}
```

Then `make manifests generate` to regenerate the CRD YAML and DeepCopy.

### 2. Helm values pass-through

In `internal/controller/registrybackend_controller.go`'s
`buildHelmValues`:

```go
"trivy": map[string]any{
    "enabled": rb.Spec.EngineConfig.Addons.Trivy.Enabled,
},
// Cosign is a hypothetical Harbor sub-component; field name matches the
// chart's values.yaml.
"cosign": map[string]any{
    "enabled": rb.Spec.EngineConfig.Addons.Cosign.Enabled,
},
```

If the addon needs more than a bool (e.g. trivy resource requests), add
fields to the `AddonToggle` struct (or sub-struct under the addon-specific
toggle) and pass them through here.

### 3. Documentation

Add a row to this file's "Available addons" table and one short paragraph
on enable/disable semantics. Update the trivy enable example with the new
addon's snippet if its on/off semantics differ.

## Why one shape per addon

All addons use `AddonToggle { Enabled bool }`. This keeps the API uniform:

- dc-api can model every addon as a single feature flag.
- The reconciler's logic is identical per addon — just look up
  `addons.<name>.enabled` and pass through.
- New addons don't need a custom struct unless they need configuration
  beyond a bool.

When an addon DOES need richer config (e.g. a list of image registries
to allow), nest it inside that addon's toggle struct:

```go
type CosignToggle struct {
    AddonToggle `json:",inline"`
    // PublicKeys is the list of cosign keys we verify against.
    PublicKeys []string `json:"publicKeys,omitempty"`
}
```

The framework stays uniform; only the addon that needs richer config
diverges in its own type.
