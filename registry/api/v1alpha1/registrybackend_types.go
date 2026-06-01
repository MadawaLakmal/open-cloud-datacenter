// Package v1alpha1 defines the registry operator's Custom Resource types.
//
// RegistryBackend represents a per-tenant Harbor cluster — the long-lived
// "backend" that hosts many user-facing Harbor-projects. One Backend per
// tenant; lives in dc-tenant-<slug>. Lifecycle managed via the Helm Go SDK
// using the vendored Harbor chart in charts/. See docs/architecture.md.

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Phase mirrors the dc-api managed-services integration contract enum (§5.1).
// +kubebuilder:validation:Enum=Pending;Provisioning;Ready;Failed;Terminating
type Phase string

const (
	PhasePending      Phase = "Pending"
	PhaseProvisioning Phase = "Provisioning"
	PhaseReady        Phase = "Ready"
	PhaseFailed       Phase = "Failed"
	PhaseTerminating  Phase = "Terminating"
)

// ---------- spec ----------

// RegistryBackendSpec describes the desired state of one tenant's Harbor cluster.
// dc-api writes this; the operator never edits it.
type RegistryBackendSpec struct {
	// HarborVersion is the Harbor Helm chart version the operator installs.
	// MUST match a tarball committed under charts/. Changing it triggers a
	// `helm upgrade` on the next reconcile.
	//
	// Bumping this value should be a reviewed PR — see docs/chart-management.md.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:default="1.14.0"
	HarborVersion string `json:"harborVersion,omitempty"`

	// CPU is the Kubernetes CPU quantity to request for Harbor's core+registry
	// pods combined.
	// +kubebuilder:default="2"
	// +optional
	CPU resource.Quantity `json:"cpu,omitempty"`

	// MemoryGB is the memory request in GiB for the Harbor pods combined.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=4
	// +optional
	MemoryGB int `json:"memoryGB,omitempty"`

	// StorageGB is the size of the PVC backing Harbor's registry storage.
	// +kubebuilder:validation:Minimum=10
	// +kubebuilder:default=100
	// +optional
	StorageGB int `json:"storageGB,omitempty"`

	// StorageClass is the Kubernetes StorageClass name for Harbor's PVCs.
	// Empty (the default) uses the cluster default — e.g. "longhorn" on a
	// standard Harvester cluster, "local-path" on k3s.
	// +optional
	StorageClass string `json:"storageClass,omitempty"`

	// EngineConfig holds Harbor-specific tuning that does not map to the
	// generic capacity dimensions above.
	// +optional
	EngineConfig BackendEngineConfig `json:"engineConfig,omitempty"`
}

// BackendEngineConfig holds Harbor-specific tuning for a Backend.
type BackendEngineConfig struct {
	// HAReplicas is the replica count for the Harbor core. v0.1 supports
	// only 1; values > 1 require a PostgreSQL + Redis HA topology that is
	// NOT YET IMPLEMENTED.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +optional
	HAReplicas int `json:"haReplicas,omitempty"`

	// ExternalURL, when set, is the URL Harbor advertises to docker clients
	// (chart `externalURL` value). Empty (default) means Harbor uses the
	// in-cluster Service DNS only; dc-api's PrivateEndpoint primitive handles
	// external exposure.
	// +optional
	ExternalURL string `json:"externalURL,omitempty"`

	// Addons controls optional Harbor sub-components. Each is opt-in.
	// +optional
	Addons BackendAddons `json:"addons,omitempty"`
}

// BackendAddons toggles optional Harbor sub-components on or off per tenant.
// dc-api exposes these as feature flags; an end user enables what they need.
// Each addon is one bool; see docs/addons.md for the rendering contract.
type BackendAddons struct {
	// Trivy enables the Trivy vulnerability scanner sidecar/StatefulSet.
	// Default: disabled.
	// +optional
	Trivy AddonToggle `json:"trivy,omitempty"`
}

// AddonToggle is the standard shape every addon uses. Keeps the API simple
// and uniform when new addons are added later.
type AddonToggle struct {
	// Enabled turns the addon on (true) or off (false). Default false.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

// ---------- status ----------

// RegistryBackendStatus is what the operator writes back.
type RegistryBackendStatus struct {
	// Phase mirrors the managed-services contract enum (§5.1).
	// +optional
	Phase Phase `json:"phase,omitempty"`

	// Conditions follow standard Kubernetes status conventions.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Endpoint is the in-cluster reachability of this Harbor. dc-api's
	// PrivateEndpoint primitive reads this to build customer-VPC proxies.
	// +optional
	Endpoint *Endpoint `json:"endpoint,omitempty"`

	// Resources tracks every child Kubernetes object the operator created
	// for idempotency on reconcile (per integration contract §5).
	// +optional
	Resources []ResourceRef `json:"resources,omitempty"`

	// InstalledVersion is the Harbor chart version actually installed.
	// Differs from spec.harborVersion during an in-flight upgrade.
	// +optional
	InstalledVersion string `json:"installedVersion,omitempty"`

	// Message is the human-readable explanation of the current phase.
	// dc-api surfaces this verbatim on GET responses.
	// +optional
	Message string `json:"message,omitempty"`

	// ObservedGeneration is the metadata.generation the operator has
	// reconciled against. Status fields are trustworthy only when
	// ObservedGeneration == metadata.generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// Endpoint matches dc-api integration contract §5 endpoint shape.
type Endpoint struct {
	// Address is the in-cluster Service DNS name (or IP) Harbor is reachable at.
	Address string `json:"address"`

	// Port is the TCP port Harbor's core service listens on.
	Port int `json:"port"`

	// SecretRef points at a Secret in the same namespace as the CR holding
	// either admin credentials (Backend) or robot credentials (Instance).
	// +optional
	SecretRef *SecretRef `json:"secretRef,omitempty"`
}

// SecretRef points at a Kubernetes Secret in the same namespace as the CR.
type SecretRef struct {
	Name string `json:"name"`
}

// ResourceRef identifies one Kubernetes object owned by the CR.
type ResourceRef struct {
	Group     string `json:"group,omitempty"`
	Version   string `json:"version"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// ---------- top-level kinds ----------

// RegistryBackend is the tenant Harbor cluster CR.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=rb
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".status.installedVersion"
// +kubebuilder:printcolumn:name="Address",type="string",JSONPath=".status.endpoint.address"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type RegistryBackend struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RegistryBackendSpec   `json:"spec,omitempty"`
	Status RegistryBackendStatus `json:"status,omitempty"`
}

// RegistryBackendList is the list form for Kubernetes list endpoints and watches.
//
// +kubebuilder:object:root=true
type RegistryBackendList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RegistryBackend `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RegistryBackend{}, &RegistryBackendList{})
}
