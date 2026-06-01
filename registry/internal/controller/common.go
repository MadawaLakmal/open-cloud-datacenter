// Package controller hosts the two reconcilers that drive the registry
// operator:
//
//   - RegistryBackendReconciler installs Harbor into a tenant namespace via
//     the vendored Helm chart.
//   - RegistryInstanceReconciler creates a Harbor-project and a robot
//     account inside a tenant's Harbor and ships credentials in a Secret.
//
// Shared helpers (finalizers, label propagation, condition upserts, random
// password generation) live here.
package controller

import (
	"crypto/rand"
	"encoding/base64"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/operators/registry/api/v1alpha1"
)

// Finalizer names — one per Kind, matching the keyvault convention of
// "<group>/<kind>-cleanup".
const (
	BackendFinalizer  = "registry.opencloud.wso2.com/backend-cleanup"
	InstanceFinalizer = "registry.opencloud.wso2.com/instance-cleanup"
)

// The seven standard labels every operator must propagate from a CR's
// metadata.labels onto every child resource it creates (Secrets, PVCs,
// ConfigMaps, etc.). These are dc-api's wire-contract labels per the
// managed-services integration spec §6 — the same keys KeyVault uses.
const (
	LabelTenant       = "dc-api.wso2.com/tenant"
	LabelProject      = "dc-api.wso2.com/project"
	LabelTenantUUID   = "dc-api.wso2.com/tenant-uuid"
	LabelProjectUUID  = "dc-api.wso2.com/project-uuid"
	LabelResourceUUID = "dc-api.wso2.com/resource-uuid"
	LabelResourceKind = "dc-api.wso2.com/resource-kind"
	LabelResourceName = "dc-api.wso2.com/resource-name"
)

// PropagatedLabelKeys is the canonical list. PropagateLabels copies whichever
// of these are non-empty on the parent CR onto the child.
var PropagatedLabelKeys = []string{
	LabelTenant, LabelProject,
	LabelTenantUUID, LabelProjectUUID,
	LabelResourceUUID, LabelResourceKind, LabelResourceName,
}

// PropagateLabels merges the seven dc-api.wso2.com/* labels from a parent
// CR's metadata onto a child label map. Existing non-conflicting labels on
// the child are preserved. Returns the (possibly newly-allocated) map.
func PropagateLabels(parent map[string]string, child map[string]string) map[string]string {
	if child == nil {
		child = map[string]string{}
	}
	for _, k := range PropagatedLabelKeys {
		if v, ok := parent[k]; ok && v != "" {
			child[k] = v
		}
	}
	return child
}

// hasFinalizer reports whether the named finalizer is present on the slice.
func hasFinalizer(fs []string, name string) bool {
	return slices.Contains(fs, name)
}

// addFinalizer returns the slice with the named finalizer appended (no-op
// if already present).
func addFinalizer(fs []string, name string) []string {
	if hasFinalizer(fs, name) {
		return fs
	}
	return append(fs, name)
}

// removeFinalizer returns the slice with the named finalizer removed
// (no-op if absent).
func removeFinalizer(fs []string, name string) []string {
	out := fs[:0]
	for _, f := range fs {
		if f != name {
			out = append(out, f)
		}
	}
	return out
}

// generatePassword returns a 24-character URL-safe random password. Used to
// seed the Harbor admin Secret on first install of a RegistryBackend.
func generatePassword() (string, error) {
	buf := make([]byte, 18) // 18 bytes → 24 chars base64
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(buf), nil
}

// makeResourceRef builds a v1alpha1.ResourceRef from common Kubernetes
// identity fields. Used to populate status.resources for idempotency.
func makeResourceRef(group, version, kind, namespace, name string) registryv1alpha1.ResourceRef {
	return registryv1alpha1.ResourceRef{
		Group:     group,
		Version:   version,
		Kind:      kind,
		Namespace: namespace,
		Name:      name,
	}
}

// setReadyCondition upserts a "Ready" condition in the supplied slice.
// LastTransitionTime is bumped only on status changes.
func setReadyCondition(conds *[]metav1.Condition, status metav1.ConditionStatus, reason, msg string, gen int64) {
	now := metav1.Now()
	for i := range *conds {
		if (*conds)[i].Type != "Ready" {
			continue
		}
		if (*conds)[i].Status != status {
			(*conds)[i].LastTransitionTime = now
		}
		(*conds)[i].Status = status
		(*conds)[i].Reason = reason
		(*conds)[i].Message = msg
		(*conds)[i].ObservedGeneration = gen
		return
	}
	*conds = append(*conds, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		Message:            msg,
		LastTransitionTime: now,
		ObservedGeneration: gen,
	})
}
