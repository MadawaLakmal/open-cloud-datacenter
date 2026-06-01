package controller

import (
	"context"
	"fmt"
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

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/operators/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/operators/registry/internal/harbor"
)

// RegistryInstanceReconciler reconciles RegistryInstance CRs. Each Instance
// maps to one Harbor-project + one robot account inside the tenant's Harbor.
type RegistryInstanceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registryinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registryinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registryinstances/finalizers,verbs=update
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registrybackends,verbs=get;list;watch

// SetupWithManager wires this reconciler into the manager.
func (r *RegistryInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&registryv1alpha1.RegistryInstance{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}

// Reconcile drives one RegistryInstance toward its spec.
func (r *RegistryInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var ri registryv1alpha1.RegistryInstance
	if err := r.Get(ctx, req.NamespacedName, &ri); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Resolve the Backend up front — we need it for both create and delete.
	backend, hc, err := r.resolveBackend(ctx, &ri)
	if err != nil {
		// During delete, if the Backend has already gone we still want to
		// drop the finalizer; otherwise requeue.
		if !ri.DeletionTimestamp.IsZero() && apierrors.IsNotFound(err) {
			ri.Finalizers = removeFinalizer(ri.Finalizers, InstanceFinalizer)
			return ctrl.Result{}, r.Update(ctx, &ri)
		}
		return ctrl.Result{}, r.failPhase(ctx, &ri, "BackendUnavailable", err)
	}

	if !ri.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &ri, hc, log)
	}

	if !hasFinalizer(ri.Finalizers, InstanceFinalizer) {
		ri.Finalizers = addFinalizer(ri.Finalizers, InstanceFinalizer)
		if err := r.Update(ctx, &ri); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if backend.Status.Phase != registryv1alpha1.PhaseReady {
		r.setPhase(&ri, registryv1alpha1.PhasePending, "BackendNotReady",
			fmt.Sprintf("backend %s is %s", backend.Name, backend.Status.Phase))
		_ = r.Status().Update(ctx, &ri)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	r.setPhase(&ri, registryv1alpha1.PhaseProvisioning, "Provisioning",
		"creating harbor project and robot")
	_ = r.Status().Update(ctx, &ri)

	// Step 1: ensure Harbor-project.
	cfg := ri.Spec.EngineConfig
	projectID, err := hc.CreateProject(ctx, ri.Spec.ProjectName, cfg.Public)
	if err != nil {
		return ctrl.Result{}, r.failPhase(ctx, &ri, "HarborProjectFailed", err)
	}
	ri.Status.HarborProjectID = projectID

	// Step 2: optional one-shot project bootstrap.
	if cfg.TagImmutability {
		if err := hc.EnableImmutableTagRule(ctx, ri.Spec.ProjectName); err != nil {
			log.Info("enable immutable tag rule (continuing)", "project", ri.Spec.ProjectName, "err", err)
		}
	}
	if cfg.Retention != nil && (cfg.Retention.KeepLastTags > 0 || cfg.Retention.UntaggedTTLDays > 0) {
		if err := hc.SetRetentionPolicy(ctx, ri.Spec.ProjectName, harbor.Retention{
			KeepLastTags:    cfg.Retention.KeepLastTags,
			UntaggedTTLDays: cfg.Retention.UntaggedTTLDays,
		}); err != nil {
			log.Info("set retention (continuing)", "project", ri.Spec.ProjectName, "err", err)
		}
	}

	// Step 3: ensure robot account + credentials Secret.
	credsSecretName := fmt.Sprintf("registry-%s-creds", string(ri.UID))
	credsSecret, robot, err := r.ensureRobotSecret(ctx, &ri, hc, credsSecretName, backend.Status.Endpoint.Address)
	if err != nil {
		return ctrl.Result{}, r.failPhase(ctx, &ri, "RobotFailed", err)
	}
	ri.Status.RobotID = robot.ID

	// Step 4: publish status.
	ri.Status.Phase = registryv1alpha1.PhaseReady
	ri.Status.Message = "registry ready"
	ri.Status.ObservedGeneration = ri.Generation
	ri.Status.Endpoint = &registryv1alpha1.Endpoint{
		Address:   backend.Status.Endpoint.Address,
		Port:      backend.Status.Endpoint.Port,
		SecretRef: &registryv1alpha1.SecretRef{Name: credsSecret.Name},
	}
	ri.Status.Resources = []registryv1alpha1.ResourceRef{
		makeResourceRef("", "v1", "Secret", credsSecret.Namespace, credsSecret.Name),
	}
	setReadyCondition(&ri.Status.Conditions, metav1.ConditionTrue,
		"InstanceProvisioned", "harbor project and robot are ready.", ri.Generation)

	if err := r.Status().Update(ctx, &ri); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// resolveBackend looks up the Backend named in spec.backendRef (using the
// namespace the CR itself carries — no derivation), then builds a Harbor
// client from its admin Secret.
func (r *RegistryInstanceReconciler) resolveBackend(ctx context.Context, ri *registryv1alpha1.RegistryInstance) (*registryv1alpha1.RegistryBackend, *harbor.Client, error) {
	backendNS := ri.Spec.BackendRef.Namespace
	backendName := ri.Spec.BackendRef.Name

	var rb registryv1alpha1.RegistryBackend
	if err := r.Get(ctx, types.NamespacedName{Namespace: backendNS, Name: backendName}, &rb); err != nil {
		return nil, nil, fmt.Errorf("get backend %s/%s: %w", backendNS, backendName, err)
	}

	if rb.Status.Endpoint == nil || rb.Status.Endpoint.SecretRef == nil {
		return &rb, nil, fmt.Errorf("backend %s/%s has no endpoint yet", backendNS, backendName)
	}

	var adminSecret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: backendNS, Name: rb.Status.Endpoint.SecretRef.Name,
	}, &adminSecret); err != nil {
		return &rb, nil, fmt.Errorf("get admin secret: %w", err)
	}

	baseURL := fmt.Sprintf("http://%s:%d", rb.Status.Endpoint.Address, rb.Status.Endpoint.Port)
	hc := harbor.New(baseURL,
		string(adminSecret.Data["username"]),
		string(adminSecret.Data["password"]),
	)
	return &rb, hc, nil
}

// ensureRobotSecret creates the robot account (if status doesn't already
// reference one) and writes its credentials to a Secret owned by the CR.
func (r *RegistryInstanceReconciler) ensureRobotSecret(
	ctx context.Context,
	ri *registryv1alpha1.RegistryInstance,
	hc *harbor.Client,
	secretName, harborAddress string,
) (*corev1.Secret, harbor.Robot, error) {
	// If the secret already exists and status already has a robot id, trust
	// they match — Harbor never tells us the secret again after create, so
	// rotation requires teardown + recreate.
	if ri.Status.RobotID != 0 {
		var existing corev1.Secret
		err := r.Get(ctx, types.NamespacedName{Namespace: ri.Namespace, Name: secretName}, &existing)
		if err == nil {
			return &existing, harbor.Robot{ID: ri.Status.RobotID}, nil
		}
		if !apierrors.IsNotFound(err) {
			return nil, harbor.Robot{}, fmt.Errorf("get creds secret: %w", err)
		}
		// Secret was deleted out-of-band — fall through to recreate the robot.
		_ = hc.DeleteRobot(ctx, ri.Spec.ProjectName, ri.Status.RobotID)
	}

	robot, err := hc.CreateRobot(ctx, ri.Spec.ProjectName,
		"robot-"+string(ri.UID), "PushPull",
		time.Now().Add(10*365*24*time.Hour))
	if err != nil {
		return nil, harbor.Robot{}, fmt.Errorf("create robot: %w", err)
	}

	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: ri.Namespace,
			Labels:    PropagateLabels(ri.Labels, nil),
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"username":   []byte(robot.Name),
			"password":   []byte(robot.Secret),
			"harbor_url": []byte(harborAddress),
		},
	}
	if err := controllerutil.SetControllerReference(ri, sec, r.Scheme); err != nil {
		return nil, harbor.Robot{}, fmt.Errorf("set owner: %w", err)
	}
	if err := r.Create(ctx, sec); err != nil {
		// Race: somebody created the Secret since our Get. Roll back the robot
		// (best effort) and bail — next reconcile picks up the existing Secret.
		_ = hc.DeleteRobot(ctx, ri.Spec.ProjectName, robot.ID)
		return nil, harbor.Robot{}, fmt.Errorf("create creds secret: %w", err)
	}
	return sec, robot, nil
}

func (r *RegistryInstanceReconciler) reconcileDelete(ctx context.Context, ri *registryv1alpha1.RegistryInstance, hc *harbor.Client, log logr.Logger) (ctrl.Result, error) {
	if !hasFinalizer(ri.Finalizers, InstanceFinalizer) {
		return ctrl.Result{}, nil
	}
	ri.Status.Phase = registryv1alpha1.PhaseTerminating
	_ = r.Status().Update(ctx, ri)

	if hc != nil {
		if ri.Status.RobotID != 0 {
			if err := hc.DeleteRobot(ctx, ri.Spec.ProjectName, ri.Status.RobotID); err != nil {
				log.Info("delete robot (continuing)", "project", ri.Spec.ProjectName, "err", err)
			}
		}
		if ri.Status.HarborProjectID != 0 {
			if err := hc.DeleteProject(ctx, ri.Status.HarborProjectID); err != nil {
				return ctrl.Result{RequeueAfter: 30 * time.Second}, err
			}
		}
	}

	ri.Finalizers = removeFinalizer(ri.Finalizers, InstanceFinalizer)
	if err := r.Update(ctx, ri); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *RegistryInstanceReconciler) failPhase(ctx context.Context, ri *registryv1alpha1.RegistryInstance, reason string, cause error) error {
	r.setPhase(ri, registryv1alpha1.PhaseFailed, reason, cause.Error())
	_ = r.Status().Update(ctx, ri)
	return cause
}

func (r *RegistryInstanceReconciler) setPhase(ri *registryv1alpha1.RegistryInstance, phase registryv1alpha1.Phase, reason, msg string) {
	ri.Status.Phase = phase
	ri.Status.Message = msg
	ri.Status.ObservedGeneration = ri.Generation
	status := metav1.ConditionFalse
	if phase == registryv1alpha1.PhaseReady {
		status = metav1.ConditionTrue
	}
	setReadyCondition(&ri.Status.Conditions, status, reason, msg, ri.Generation)
}
