package controller

import (
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/operators/registry/api/v1alpha1"
)

// testBackendCR returns a RegistryBackend shaped like what dc-api would create:
// a per-tenant namespace, the seven dc-api.wso2.com/* labels stamped at creation
// time, and a sensible default capacity.
func testBackendCR() *registryv1alpha1.RegistryBackend {
	return &registryv1alpha1.RegistryBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "harbor",
			Namespace: "dc-tenant-acme",
			Labels: map[string]string{
				"dc-api.wso2.com/tenant":        "acme",
				"dc-api.wso2.com/tenant-uuid":   "abcd1234-0000-0000-0000-000000000001",
				"dc-api.wso2.com/resource-uuid": "abcd1234-0000-0000-0000-000000000002",
				"dc-api.wso2.com/resource-kind": "registry-backend",
				"dc-api.wso2.com/resource-name": "harbor",
				"my.private.label":              "should-stay-here",
			},
		},
		Spec: registryv1alpha1.RegistryBackendSpec{
			HarborVersion: "1.14.0",
			StorageGB:     100,
			StorageClass:  "longhorn",
		},
	}
}

// TestBuildHelmValues_BasicShape validates the structural invariants of the
// values map that every Backend reconcile passes to the vendored Harbor chart.
func TestBuildHelmValues_BasicShape(t *testing.T) {
	r := &RegistryBackendReconciler{}
	cr := testBackendCR()
	values := r.buildHelmValues(cr, "s3cr3t")

	// expose must always be clusterIP with TLS disabled.
	expose, ok := values["expose"].(map[string]any)
	if !ok {
		t.Fatal("expose: expected map[string]any")
	}
	if got := expose["type"]; got != "clusterIP" {
		t.Errorf("expose.type = %q, want clusterIP", got)
	}
	tls, ok := expose["tls"].(map[string]any)
	if !ok {
		t.Fatal("expose.tls: expected map[string]any")
	}
	if got := tls["enabled"]; got != false {
		t.Errorf("expose.tls.enabled = %v, want false", got)
	}

	// Admin password must be wired through verbatim.
	if got := values["harborAdminPassword"]; got != "s3cr3t" {
		t.Errorf("harborAdminPassword = %q, want s3cr3t", got)
	}

	// Persistence must be enabled.
	persistence, ok := values["persistence"].(map[string]any)
	if !ok {
		t.Fatal("persistence: expected map[string]any")
	}
	if got := persistence["enabled"]; got != true {
		t.Errorf("persistence.enabled = %v, want true", got)
	}
	if _, ok := persistence["persistentVolumeClaim"]; !ok {
		t.Error("persistence.persistentVolumeClaim key missing")
	}
}

// TestBuildHelmValues_ExternalURLDefault validates that when no ExternalURL
// override is set, the default points to the in-cluster Harbor core Service DNS.
func TestBuildHelmValues_ExternalURLDefault(t *testing.T) {
	r := &RegistryBackendReconciler{}
	cr := testBackendCR()
	cr.Spec.EngineConfig.ExternalURL = ""
	values := r.buildHelmValues(cr, "pw")

	want := fmt.Sprintf("http://%s-harbor-core.%s.svc.cluster.local", HelmReleaseName, cr.Namespace)
	if got := values["externalURL"]; got != want {
		t.Errorf("externalURL = %q, want %q", got, want)
	}
}

// TestBuildHelmValues_ExternalURLOverride validates that spec.engineConfig.externalURL
// replaces the in-cluster default when set.
func TestBuildHelmValues_ExternalURLOverride(t *testing.T) {
	r := &RegistryBackendReconciler{}
	cr := testBackendCR()
	cr.Spec.EngineConfig.ExternalURL = "https://registry.example.com"
	values := r.buildHelmValues(cr, "pw")

	if got := values["externalURL"]; got != "https://registry.example.com" {
		t.Errorf("externalURL = %q, want https://registry.example.com", got)
	}
}

// TestBuildHelmValues_TrivyDisabledByDefault validates that Trivy is off when
// the addon is not explicitly enabled, so tenants don't pay the resource cost
// without opting in.
func TestBuildHelmValues_TrivyDisabledByDefault(t *testing.T) {
	r := &RegistryBackendReconciler{}
	cr := testBackendCR()
	// Trivy addon not set — zero-value AddonToggle.Enabled is false.
	values := r.buildHelmValues(cr, "pw")

	trivy, ok := values["trivy"].(map[string]any)
	if !ok {
		t.Fatal("trivy: expected map[string]any")
	}
	if got := trivy["enabled"]; got != false {
		t.Errorf("trivy.enabled = %v, want false (disabled by default)", got)
	}
}

// TestBuildHelmValues_TrivyEnabled validates that spec.engineConfig.addons.trivy.enabled=true
// propagates to the Helm values correctly.
func TestBuildHelmValues_TrivyEnabled(t *testing.T) {
	r := &RegistryBackendReconciler{}
	cr := testBackendCR()
	cr.Spec.EngineConfig.Addons.Trivy = registryv1alpha1.AddonToggle{Enabled: true}
	values := r.buildHelmValues(cr, "pw")

	trivy := values["trivy"].(map[string]any)
	if got := trivy["enabled"]; got != true {
		t.Errorf("trivy.enabled = %v, want true", got)
	}
}

// TestBuildHelmValues_HAReplicas_NotPassedToHelm documents that haReplicas (capped
// at 1 by the CRD in v0.1) has no representation in the Helm values map. When
// HA topology is implemented this test must be updated to assert core.replicas,
// registry.replicas, and jobservice.replicas keys.
func TestBuildHelmValues_HAReplicas_NotPassedToHelm(t *testing.T) {
	r := &RegistryBackendReconciler{}
	cr := testBackendCR()
	cr.Spec.EngineConfig.HAReplicas = 1
	values := r.buildHelmValues(cr, "pw")

	for _, key := range []string{"core", "registry", "jobservice", "portal"} {
		if _, ok := values[key]; ok {
			t.Errorf("values must not contain %q until HA is wired; found %v", key, values[key])
		}
	}
}

// TestHarborPVCs_NoStorageClass validates that when no StorageClass is specified
// only the registry PVC is emitted (with the correct size), and no storageClass
// key is present so Kubernetes uses the cluster default.
func TestHarborPVCs_NoStorageClass(t *testing.T) {
	pvcs := harborPVCs(50, "")

	registry, ok := pvcs["registry"].(map[string]any)
	if !ok {
		t.Fatal("registry pvc: expected map[string]any")
	}
	if got := registry["size"]; got != "50Gi" {
		t.Errorf("registry.size = %q, want 50Gi", got)
	}
	if _, present := registry["storageClass"]; present {
		t.Error("registry.storageClass must be absent when StorageClass is empty (use cluster default)")
	}

	// Sub-component PVCs must not appear — their sizes stay at chart defaults.
	for _, name := range []string{"jobservice", "database", "redis", "trivy"} {
		if _, present := pvcs[name]; present {
			t.Errorf("pvc %q must not be set when StorageClass is empty", name)
		}
	}
}

// TestHarborPVCs_WithStorageClass validates that when a StorageClass is given it
// is applied uniformly to all five Harbor PVC sub-components, and that the
// registry PVC gets the correct capacity.
func TestHarborPVCs_WithStorageClass(t *testing.T) {
	pvcs := harborPVCs(200, "longhorn")

	registry, ok := pvcs["registry"].(map[string]any)
	if !ok {
		t.Fatal("registry pvc: expected map[string]any")
	}
	if got := registry["size"]; got != "200Gi" {
		t.Errorf("registry.size = %q, want 200Gi", got)
	}
	if got := registry["storageClass"]; got != "longhorn" {
		t.Errorf("registry.storageClass = %q, want longhorn", got)
	}

	// All sub-component PVCs must carry the same StorageClass so every piece
	// of Harbor lands on the same storage backend.
	for _, name := range []string{"jobservice", "database", "redis", "trivy"} {
		pvc, ok := pvcs[name].(map[string]any)
		if !ok {
			t.Fatalf("pvc %q: expected map[string]any", name)
		}
		if got := pvc["storageClass"]; got != "longhorn" {
			t.Errorf("pvc %q storageClass = %q, want longhorn", name, got)
		}
	}
}
