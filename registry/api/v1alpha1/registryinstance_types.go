// RegistryInstance represents one Harbor-project inside a tenant Harbor.
// One RegistryInstance per dc-api project; lives in dc-<tenant>-<project>.
// The operator creates the named Harbor-project + a robot account by calling
// the Harbor API of the referenced RegistryBackend.

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ---------- spec ----------

// RegistryInstanceSpec captures the desired Harbor-project.
type RegistryInstanceSpec struct {
	// BackendRef points at the RegistryBackend for this Resource's tenant.
	// dc-api sets this; users never specify it. Matches the KeyVault BackendRef
	// shape so dc-api treats both kinds uniformly.
	// +required
	BackendRef BackendReference `json:"backendRef"`

	// ProjectName is the Harbor-project name to create inside the tenant
	// Harbor. Must be unique within that Harbor.
	// Lowercase letters, digits, hyphens (Harbor's own naming rules).
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	ProjectName string `json:"projectName"`

	// EngineConfig holds Harbor-project-level knobs.
	// +optional
	EngineConfig InstanceEngineConfig `json:"engineConfig,omitempty"`
}

// BackendReference points at a RegistryBackend CR. Both fields are required:
// dc-api fills the namespace, so the operator hardcodes no naming pattern.
type BackendReference struct {
	// Name of the RegistryBackend CR.
	// +required
	Name string `json:"name"`

	// Namespace of the RegistryBackend CR (typically dc-tenant-<slug>).
	// +required
	Namespace string `json:"namespace"`
}

// InstanceEngineConfig is Harbor-project-level configuration.
type InstanceEngineConfig struct {
	// Public makes the Harbor-project publicly readable (anonymous pull).
	// Default false. Independent of dc-api's PrivateEndpoint exposure.
	// +kubebuilder:default=false
	// +optional
	Public bool `json:"public,omitempty"`

	// TagImmutability, when true, applies a "match all" immutability rule
	// to the project on first reconcile.
	// +kubebuilder:default=false
	// +optional
	TagImmutability bool `json:"tagImmutability,omitempty"`

	// Retention is the optional retention policy applied at provision time.
	// +optional
	Retention *Retention `json:"retention,omitempty"`
}

// Retention bounds how many tags / how long untagged artefacts are kept.
type Retention struct {
	// KeepLastTags keeps the N most-recent tags per repository. 0 = unset.
	// +kubebuilder:validation:Minimum=0
	// +optional
	KeepLastTags int `json:"keepLastTags,omitempty"`

	// UntaggedTTLDays deletes untagged artefacts older than this many days. 0 = unset.
	// +kubebuilder:validation:Minimum=0
	// +optional
	UntaggedTTLDays int `json:"untaggedTTLDays,omitempty"`
}

// ---------- status ----------

// RegistryInstanceStatus is what the operator writes back.
type RegistryInstanceStatus struct {
	// Phase mirrors the managed-services contract enum.
	// +optional
	Phase Phase `json:"phase,omitempty"`

	// Conditions follow standard Kubernetes status conventions.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Endpoint.Address + Port come from the referenced RegistryBackend's status.
	// Endpoint.SecretRef points at this Instance's robot-credentials Secret
	// in the same namespace as the CR.
	// +optional
	Endpoint *Endpoint `json:"endpoint,omitempty"`

	// Resources tracks owned objects (the robot-credentials Secret) for idempotency.
	// +optional
	Resources []ResourceRef `json:"resources,omitempty"`

	// HarborProjectID is the numeric Harbor-project ID returned by the create API.
	// Used by the deletion finalizer.
	// +optional
	HarborProjectID int `json:"harborProjectId,omitempty"`

	// RobotID is the numeric Harbor robot ID for the credentials in endpoint.secretRef.
	// Used by the deletion finalizer.
	// +optional
	RobotID int `json:"robotId,omitempty"`

	// Message is the human-readable status, surfaced verbatim by dc-api on GET.
	// +optional
	Message string `json:"message,omitempty"`

	// ObservedGeneration is the metadata.generation the operator has reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// ---------- top-level kinds ----------

// RegistryInstance is the per-project Harbor-project CR.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=ri
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Project",type="string",JSONPath=".spec.projectName"
// +kubebuilder:printcolumn:name="Backend",type="string",JSONPath=".spec.backendRef.name"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Address",type="string",JSONPath=".status.endpoint.address"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type RegistryInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RegistryInstanceSpec   `json:"spec,omitempty"`
	Status RegistryInstanceStatus `json:"status,omitempty"`
}

// RegistryInstanceList is the list form for Kubernetes list endpoints and watches.
//
// +kubebuilder:object:root=true
type RegistryInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RegistryInstance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RegistryInstance{}, &RegistryInstanceList{})
}
