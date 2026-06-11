package controller

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"helm.sh/helm/v3/pkg/release"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/operators/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/operators/registry/internal/helmrunner"
)

// HelmReleaseName is the fixed Helm release name used inside every tenant
// namespace. Each tenant gets exactly one Harbor; the tenant slug is already
// encoded by the namespace (e.g. dc-tenant-acme).
const HelmReleaseName = "harbor"

// HelmEnsurer is the interface the Backend reconciler uses to install and
// uninstall Harbor. *helmrunner.Runner satisfies it; tests inject a fake.
type HelmEnsurer interface {
	Ensure(ctx context.Context, opts helmrunner.InstallOptions) (*release.Release, error)
	Uninstall(ctx context.Context, releaseName, namespace string) error
}

// RegistryBackendReconciler reconciles RegistryBackend CRs. One RegistryBackend
// corresponds to one Harbor cluster Helm-installed into a tenant namespace.
type RegistryBackendReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Helm   HelmEnsurer

	// HarborInstallTimeout bounds each helm install/upgrade. Default 10m.
	// Configurable via the manager's Deployment env.
	HarborInstallTimeout time.Duration
}

// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registrybackends,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registrybackends/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registrybackends/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps;serviceaccounts;services;persistentvolumeclaims;pods;events;endpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets;daemonsets;replicasets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses;networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// SetupWithManager wires this reconciler into the manager.
func (r *RegistryBackendReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.HarborInstallTimeout == 0 {
		r.HarborInstallTimeout = 10 * time.Minute
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&registryv1alpha1.RegistryBackend{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}

// Reconcile drives one RegistryBackend toward its spec.
func (r *RegistryBackendReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var rb registryv1alpha1.RegistryBackend
	if err := r.Get(ctx, req.NamespacedName, &rb); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !rb.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &rb, log)
	}

	if !hasFinalizer(rb.Finalizers, BackendFinalizer) {
		rb.Finalizers = addFinalizer(rb.Finalizers, BackendFinalizer)
		if err := r.Update(ctx, &rb); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Step 1: ensure the admin Secret.
	adminSecret, err := r.ensureAdminSecret(ctx, &rb)
	if err != nil {
		return r.failPhase(ctx, &rb, "AdminSecretFailed", err)
	}

	// Step 2: announce provisioning before the blocking helm call.
	r.setPhase(&rb, registryv1alpha1.PhaseProvisioning, "HelmInProgress",
		fmt.Sprintf("installing harbor %s", rb.Spec.HarborVersion))
	if err := r.Status().Update(ctx, &rb); err != nil {
		return ctrl.Result{}, err
	}

	// Step 3: helm install or upgrade. Vendored chart is loaded by helmrunner.
	values := r.buildHelmValues(&rb, string(adminSecret.Data["password"]))
	if _, err := r.Helm.Ensure(ctx, helmrunner.InstallOptions{
		ReleaseName:  HelmReleaseName,
		Namespace:    rb.Namespace,
		ChartVersion: rb.Spec.HarborVersion,
		Values:       values,
		Timeout:      r.HarborInstallTimeout,
	}); err != nil {
		return r.failPhase(ctx, &rb, "HelmFailed", err)
	}

	// Step 4: publish status. Harbor chart names its core Service
	// "<release>-harbor-core"; port 80 (HTTP). dc-api's PrivateEndpoint
	// primitive handles external TLS termination.
	addr := fmt.Sprintf("%s-harbor-core.%s.svc.cluster.local", HelmReleaseName, rb.Namespace)
	rb.Status.Phase = registryv1alpha1.PhaseReady
	rb.Status.InstalledVersion = rb.Spec.HarborVersion
	rb.Status.Endpoint = &registryv1alpha1.Endpoint{
		Address:   addr,
		Port:      80,
		SecretRef: &registryv1alpha1.SecretRef{Name: adminSecret.Name},
	}
	rb.Status.Message = "harbor ready"
	rb.Status.ObservedGeneration = rb.Generation
	rb.Status.Resources = []registryv1alpha1.ResourceRef{
		makeResourceRef("", "v1", "Secret", adminSecret.Namespace, adminSecret.Name),
		makeResourceRef("", "v1", "Service", rb.Namespace, HelmReleaseName+"-harbor-core"),
	}
	setReadyCondition(&rb.Status.Conditions, metav1.ConditionTrue,
		"HarborReady", "Harbor pods are running.", rb.Generation)

	if err := r.Status().Update(ctx, &rb); err != nil {
		return ctrl.Result{}, err
	}

	// Re-reconcile periodically to detect drift.
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *RegistryBackendReconciler) reconcileDelete(ctx context.Context, rb *registryv1alpha1.RegistryBackend, log logr.Logger) (ctrl.Result, error) {
	if !hasFinalizer(rb.Finalizers, BackendFinalizer) {
		return ctrl.Result{}, nil
	}
	rb.Status.Phase = registryv1alpha1.PhaseTerminating
	rb.Status.Message = "uninstalling harbor"
	_ = r.Status().Update(ctx, rb)

	if err := r.Helm.Uninstall(ctx, HelmReleaseName, rb.Namespace); err != nil {
		log.Error(err, "helm uninstall failed", "namespace", rb.Namespace)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	rb.Finalizers = removeFinalizer(rb.Finalizers, BackendFinalizer)
	if err := r.Update(ctx, rb); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// ensureAdminSecret reads or generates the Harbor admin Secret for this
// Backend. The Secret is owned by the CR so it is garbage-collected on delete.
// Name convention: <release>-admin-credentials.
func (r *RegistryBackendReconciler) ensureAdminSecret(ctx context.Context, rb *registryv1alpha1.RegistryBackend) (*corev1.Secret, error) {
	name := HelmReleaseName + "-admin-credentials"
	var sec corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Namespace: rb.Namespace, Name: name}, &sec)
	if err == nil {
		return &sec, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get admin secret: %w", err)
	}

	pw, err := generatePassword()
	if err != nil {
		return nil, fmt.Errorf("generate admin password: %w", err)
	}

	sec = corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: rb.Namespace,
			Labels:    PropagateLabels(rb.Labels, nil),
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"username": []byte("admin"),
			"password": []byte(pw),
		},
	}
	if err := controllerutil.SetControllerReference(rb, &sec, r.Scheme); err != nil {
		return nil, fmt.Errorf("set owner: %w", err)
	}
	if err := r.Create(ctx, &sec); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Race with another reconcile — re-read and return.
			if err2 := r.Get(ctx, types.NamespacedName{Namespace: rb.Namespace, Name: name}, &sec); err2 == nil {
				return &sec, nil
			}
		}
		return nil, fmt.Errorf("create admin secret: %w", err)
	}
	return &sec, nil
}

// buildHelmValues renders the values map passed to the vendored Harbor chart.
//
// We set the few values we always want to control (admin password, in-cluster
// expose, storage class) plus the addon toggles. Everything else is left to
// the chart's defaults plus charts/values-baseline.yaml.
func (r *RegistryBackendReconciler) buildHelmValues(rb *registryv1alpha1.RegistryBackend, adminPassword string) map[string]any {
	values := map[string]any{
		"expose": map[string]any{
			"type": "clusterIP",
			"tls":  map[string]any{"enabled": false},
		},
		"externalURL":         fmt.Sprintf("http://%s-harbor-core.%s.svc.cluster.local", HelmReleaseName, rb.Namespace),
		"harborAdminPassword": adminPassword,
		"persistence": map[string]any{
			"enabled":               true,
			"persistentVolumeClaim": harborPVCs(rb.Spec.StorageGB, rb.Spec.StorageClass),
		},
		// Default-disable addons; per-addon overrides follow.
		"trivy": map[string]any{
			"enabled": rb.Spec.EngineConfig.Addons.Trivy.Enabled,
		},
	}
	if rb.Spec.EngineConfig.ExternalURL != "" {
		values["externalURL"] = rb.Spec.EngineConfig.ExternalURL
	}
	return values
}

// harborPVCs builds the persistentVolumeClaim block for every Harbor
// sub-component that owns a PVC. The registry component gets storageGB GiB;
// the rest use small chart defaults. StorageClass is applied uniformly when
// set; omitted otherwise so the cluster default StorageClass binds.
func harborPVCs(storageGB int, storageClass string) map[string]any {
	registry := map[string]any{"size": strconv.Itoa(storageGB) + "Gi"}
	if storageClass != "" {
		registry["storageClass"] = storageClass
	}
	pvcs := map[string]any{"registry": registry}
	if storageClass != "" {
		// Apply the same class to every other sub-PVC so the install lands on
		// one storage backend. Sizes stay at chart defaults.
		for _, name := range []string{"jobservice", "database", "redis", "trivy"} {
			pvcs[name] = map[string]any{"storageClass": storageClass}
		}
	}
	return pvcs
}

// failPhase marks the CR as Failed with a message and returns the err for the
// controller-runtime caller (so it requeues with backoff).
func (r *RegistryBackendReconciler) failPhase(ctx context.Context, rb *registryv1alpha1.RegistryBackend, reason string, cause error) (ctrl.Result, error) {
	r.setPhase(rb, registryv1alpha1.PhaseFailed, reason, cause.Error())
	_ = r.Status().Update(ctx, rb)
	return ctrl.Result{}, cause
}

func (r *RegistryBackendReconciler) setPhase(rb *registryv1alpha1.RegistryBackend, phase registryv1alpha1.Phase, reason, msg string) {
	rb.Status.Phase = phase
	rb.Status.Message = msg
	rb.Status.ObservedGeneration = rb.Generation
	status := metav1.ConditionFalse
	if phase == registryv1alpha1.PhaseReady {
		status = metav1.ConditionTrue
	}
	setReadyCondition(&rb.Status.Conditions, status, reason, msg, rb.Generation)
}
