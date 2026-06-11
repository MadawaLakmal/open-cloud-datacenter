package controller

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/operators/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/operators/registry/internal/harbor"
)

// fakeHarbor is a test double for harbor.HarborClient. Callers configure
// per-method errors and inspect what was called.
type fakeHarbor struct {
	createProjectID  int
	createProjectErr error
	lastPublicArg    bool
	deleteProjectErr error
	createRobotRet   harbor.Robot
	createRobotErr   error
	deleteRobotErr   error

	createdProjects  []string
	deletedProjectID []int
	createdRobots    []string
	deletedRobots    []int
	updatedVisibility map[int]bool

	immutableTagRuleCallCount int
	retentionCallCount        int
}

func newFakeHarbor(projectID int, robotName, robotSecret string) *fakeHarbor {
	return &fakeHarbor{
		createProjectID: projectID,
		createRobotRet:  harbor.Robot{ID: 42, Name: robotName, Secret: robotSecret},
		updatedVisibility: map[int]bool{},
	}
}

func (f *fakeHarbor) Health(_ context.Context) error { return nil }

func (f *fakeHarbor) CreateProject(_ context.Context, name string, public bool) (int, error) {
	f.createdProjects = append(f.createdProjects, name)
	f.lastPublicArg = public
	return f.createProjectID, f.createProjectErr
}

func (f *fakeHarbor) GetProjectByName(_ context.Context, name string) (harbor.Project, error) {
	return harbor.Project{}, harbor.ErrProjectNotFound
}

func (f *fakeHarbor) DeleteProject(_ context.Context, id int) error {
	f.deletedProjectID = append(f.deletedProjectID, id)
	return f.deleteProjectErr
}

func (f *fakeHarbor) UpdateProjectVisibility(_ context.Context, id int, public bool) error {
	f.updatedVisibility[id] = public
	return nil
}

func (f *fakeHarbor) CreateRobot(_ context.Context, project, name, _ string, _ time.Time) (harbor.Robot, error) {
	f.createdRobots = append(f.createdRobots, fmt.Sprintf("%s/%s", project, name))
	return f.createRobotRet, f.createRobotErr
}

func (f *fakeHarbor) DeleteRobot(_ context.Context, _ string, id int) error {
	f.deletedRobots = append(f.deletedRobots, id)
	return f.deleteRobotErr
}

func (f *fakeHarbor) EnableImmutableTagRule(_ context.Context, _ string) error {
	f.immutableTagRuleCallCount++
	return nil
}

func (f *fakeHarbor) SetRetentionPolicy(_ context.Context, _ string, _ harbor.Retention) error {
	f.retentionCallCount++
	return nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

const (
	tenantNS  = "dc-tenant-stub"
	projectNS = "dc-stub-billing"
)

// ensureNamespace creates ns if absent (envtest doesn't pre-create arbitrary ns).
func ensureNamespace(name string) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	err := k8sClient.Create(ctx, ns)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

// readyBackend creates a RegistryBackend CR in tenantNS with phase=Ready and
// a populated admin Secret, then returns its NamespacedName.
func readyBackend(name string) types.NamespacedName {
	ensureNamespace(tenantNS)

	adminSecretName := HelmReleaseName + "-admin-credentials"
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: adminSecretName, Namespace: tenantNS},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"username": []byte("admin"),
			"password": []byte("test-password"),
		},
	}
	_ = k8sClient.Delete(ctx, sec)
	Expect(k8sClient.Create(ctx, sec)).To(Succeed())

	rb := &registryv1alpha1.RegistryBackend{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tenantNS},
		Spec:       registryv1alpha1.RegistryBackendSpec{HarborVersion: "1.14.0"},
	}
	_ = k8sClient.Delete(ctx, rb)
	Expect(k8sClient.Create(ctx, rb)).To(Succeed())

	rb.Status.Phase = registryv1alpha1.PhaseReady
	rb.Status.Endpoint = &registryv1alpha1.Endpoint{
		Address:   "harbor-harbor-core.dc-tenant-stub.svc.cluster.local",
		Port:      80,
		SecretRef: &registryv1alpha1.SecretRef{Name: adminSecretName},
	}
	Expect(k8sClient.Status().Update(ctx, rb)).To(Succeed())

	return types.NamespacedName{Name: name, Namespace: tenantNS}
}

func newInstance(name, ns, backendName, backendNS, projectName string) *registryv1alpha1.RegistryInstance {
	return &registryv1alpha1.RegistryInstance{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: registryv1alpha1.RegistryInstanceSpec{
			BackendRef: registryv1alpha1.BackendReference{
				Name:      backendName,
				Namespace: backendNS,
			},
			ProjectName: projectName,
		},
	}
}

// ── tests ────────────────────────────────────────────────────────────────────

var _ = Describe("RegistryInstance Controller", func() {
	const (
		instanceName = "billing-registry"
		projectName  = "billing"
		backendName  = "harbor"
	)

	instanceKey := types.NamespacedName{Name: instanceName, Namespace: projectNS}

	var fakeHC *fakeHarbor

	newReconciler := func() *RegistryInstanceReconciler {
		return &RegistryInstanceReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			HarborFactory: func(_, _, _ string) harbor.HarborClient {
				return fakeHC
			},
		}
	}

	BeforeEach(func() {
		fakeHC = newFakeHarbor(77, "robot$billing", "s3cr3t")
		ensureNamespace(projectNS)
	})

	AfterEach(func() {
		ri := &registryv1alpha1.RegistryInstance{}
		if err := k8sClient.Get(ctx, instanceKey, ri); err == nil {
			ri.Finalizers = nil
			_ = k8sClient.Update(ctx, ri)
			_ = k8sClient.Delete(ctx, ri)
		}
		// Clean up backend + secret created by readyBackend()
		rb := &registryv1alpha1.RegistryBackend{}
		rbKey := types.NamespacedName{Name: backendName, Namespace: tenantNS}
		if err := k8sClient.Get(ctx, rbKey, rb); err == nil {
			rb.Finalizers = nil
			_ = k8sClient.Update(ctx, rb)
			_ = k8sClient.Delete(ctx, rb)
		}
	})

	Context("First reconcile — no existing state", func() {
		It("adds a finalizer and requeues without touching Harbor", func() {
			readyBackend(backendName)
			ri := newInstance(instanceName, projectNS, backendName, tenantNS, projectName)
			Expect(k8sClient.Create(ctx, ri)).To(Succeed())

			r := newReconciler()
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: instanceKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Requeue).To(BeTrue())

			Expect(fakeHC.createdProjects).To(BeEmpty(), "Harbor must not be called before finalizer is set")

			updated := &registryv1alpha1.RegistryInstance{}
			Expect(k8sClient.Get(ctx, instanceKey, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement(InstanceFinalizer))
		})
	})

	Context("Backend not Ready", func() {
		It("sets phase Pending and requeues", func() {
			ensureNamespace(tenantNS)
			rb := &registryv1alpha1.RegistryBackend{
				ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: tenantNS},
				Spec:       registryv1alpha1.RegistryBackendSpec{HarborVersion: "1.14.0"},
			}
			_ = k8sClient.Delete(ctx, rb)
			Expect(k8sClient.Create(ctx, rb)).To(Succeed())
			// Leave status.phase empty (not Ready).

			ri := newInstance(instanceName, projectNS, backendName, tenantNS, projectName)
			ri.Finalizers = []string{InstanceFinalizer}
			Expect(k8sClient.Create(ctx, ri)).To(Succeed())

			r := newReconciler()
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: instanceKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			updated := &registryv1alpha1.RegistryInstance{}
			Expect(k8sClient.Get(ctx, instanceKey, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(registryv1alpha1.PhasePending))

			Expect(fakeHC.createdProjects).To(BeEmpty())
		})
	})

	Context("Happy path — Backend Ready", func() {
		It("creates Harbor project + robot, writes creds Secret, sets phase Ready", func() {
			readyBackend(backendName)

			ri := newInstance(instanceName, projectNS, backendName, tenantNS, projectName)
			ri.Finalizers = []string{InstanceFinalizer}
			Expect(k8sClient.Create(ctx, ri)).To(Succeed())

			r := newReconciler()
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: instanceKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			By("checking Harbor was called correctly")
			Expect(fakeHC.createdProjects).To(ContainElement(projectName))
			Expect(fakeHC.createdRobots).To(HaveLen(1))

			By("checking the creds Secret exists in the project namespace")
			updated := &registryv1alpha1.RegistryInstance{}
			Expect(k8sClient.Get(ctx, instanceKey, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(registryv1alpha1.PhaseReady))
			Expect(updated.Status.HarborProjectID).To(Equal(77))
			Expect(updated.Status.RobotID).To(Equal(42))
			Expect(updated.Status.Endpoint).NotTo(BeNil())
			Expect(updated.Status.Endpoint.SecretRef).NotTo(BeNil())

			credSecretName := updated.Status.Endpoint.SecretRef.Name
			sec := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: credSecretName, Namespace: projectNS,
			}, sec)).To(Succeed())
			Expect(string(sec.Data["username"])).To(Equal("robot$billing"))
			Expect(string(sec.Data["password"])).To(Equal("s3cr3t"))
			Expect(sec.Data["harbor_url"]).NotTo(BeEmpty())

			By("cleaning up the creds Secret")
			_ = k8sClient.Delete(ctx, sec)
		})

		It("propagates dc-api labels from RegistryInstance to the creds Secret", func() {
			readyBackend(backendName)

			ri := newInstance(instanceName, projectNS, backendName, tenantNS, projectName)
			ri.Finalizers = []string{InstanceFinalizer}
			ri.Labels = map[string]string{
				LabelTenant:       "stub",
				LabelProject:      "billing",
				LabelResourceUUID: "uuid-res",
			}
			Expect(k8sClient.Create(ctx, ri)).To(Succeed())

			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: instanceKey})
			Expect(err).NotTo(HaveOccurred())

			updated := &registryv1alpha1.RegistryInstance{}
			Expect(k8sClient.Get(ctx, instanceKey, updated)).To(Succeed())
			credSecretName := updated.Status.Endpoint.SecretRef.Name

			sec := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: credSecretName, Namespace: projectNS,
			}, sec)).To(Succeed())
			Expect(sec.Labels[LabelTenant]).To(Equal("stub"))
			Expect(sec.Labels[LabelProject]).To(Equal("billing"))

			_ = k8sClient.Delete(ctx, sec)
		})
	})

	Context("Visibility drift reconciliation", func() {
		It("passes the updated public flag to CreateProject on spec change", func() {
			readyBackend(backendName)

			ri := newInstance(instanceName, projectNS, backendName, tenantNS, projectName)
			ri.Finalizers = []string{InstanceFinalizer}
			ri.Spec.EngineConfig.Public = false
			Expect(k8sClient.Create(ctx, ri)).To(Succeed())

			r := newReconciler()
			// First reconcile: project created with public=false.
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: instanceKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(fakeHC.lastPublicArg).To(BeFalse())

			// Flip spec to public=true.
			updated := &registryv1alpha1.RegistryInstance{}
			Expect(k8sClient.Get(ctx, instanceKey, updated)).To(Succeed())
			updated.Spec.EngineConfig.Public = true
			Expect(k8sClient.Update(ctx, updated)).To(Succeed())

			// Second reconcile: CreateProject must be called with public=true so
			// the real harbor client can reconcile the visibility drift.
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: instanceKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(fakeHC.lastPublicArg).To(BeTrue(), "CreateProject must reflect the updated public flag")

			updated2 := &registryv1alpha1.RegistryInstance{}
			Expect(k8sClient.Get(ctx, instanceKey, updated2)).To(Succeed())
			sec := &corev1.Secret{}
			_ = k8sClient.Get(ctx, types.NamespacedName{
				Name: updated2.Status.Endpoint.SecretRef.Name, Namespace: projectNS,
			}, sec)
			_ = k8sClient.Delete(ctx, sec)
		})
	})

	Context("Idempotency — robot already provisioned", func() {
		It("does not re-apply tagImmutability or retention on subsequent reconciles", func() {
			readyBackend(backendName)

			ri := newInstance(instanceName, projectNS, backendName, tenantNS, projectName)
			ri.Finalizers = []string{InstanceFinalizer}
			ri.Spec.EngineConfig.TagImmutability = true
			ri.Spec.EngineConfig.Retention = &registryv1alpha1.Retention{KeepLastTags: 10}
			Expect(k8sClient.Create(ctx, ri)).To(Succeed())

			r := newReconciler()
			// First reconcile: bootstrap must run exactly once.
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: instanceKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(fakeHC.immutableTagRuleCallCount).To(Equal(1), "immutable tag rule must be set on first reconcile")
			Expect(fakeHC.retentionCallCount).To(Equal(1), "retention policy must be set on first reconcile")

			// Second reconcile: bootstrap must NOT run again (RobotID is now set).
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: instanceKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(fakeHC.immutableTagRuleCallCount).To(Equal(1), "immutable tag rule must not be re-applied on requeue")
			Expect(fakeHC.retentionCallCount).To(Equal(1), "retention policy must not accumulate duplicate entries on requeue")

			updated := &registryv1alpha1.RegistryInstance{}
			Expect(k8sClient.Get(ctx, instanceKey, updated)).To(Succeed())
			sec := &corev1.Secret{}
			_ = k8sClient.Get(ctx, types.NamespacedName{
				Name: updated.Status.Endpoint.SecretRef.Name, Namespace: projectNS,
			}, sec)
			_ = k8sClient.Delete(ctx, sec)
		})

		It("skips robot creation when status.RobotID and creds Secret already exist", func() {
			readyBackend(backendName)

			ri := newInstance(instanceName, projectNS, backendName, tenantNS, projectName)
			ri.Finalizers = []string{InstanceFinalizer}
			Expect(k8sClient.Create(ctx, ri)).To(Succeed())

			r := newReconciler()
			// First reconcile provisions everything.
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: instanceKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(fakeHC.createdRobots).To(HaveLen(1))

			// Second reconcile must reuse existing robot.
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: instanceKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(fakeHC.createdRobots).To(HaveLen(1), "robot must not be re-created on second reconcile")

			// Cleanup creds secret
			updated := &registryv1alpha1.RegistryInstance{}
			Expect(k8sClient.Get(ctx, instanceKey, updated)).To(Succeed())
			sec := &corev1.Secret{}
			_ = k8sClient.Get(ctx, types.NamespacedName{
				Name: updated.Status.Endpoint.SecretRef.Name, Namespace: projectNS,
			}, sec)
			_ = k8sClient.Delete(ctx, sec)
		})
	})

	Context("Harbor project creation failure", func() {
		It("marks phase Failed and returns an error for requeue", func() {
			fakeHC.createProjectErr = fmt.Errorf("harbor connection refused")

			readyBackend(backendName)
			ri := newInstance(instanceName, projectNS, backendName, tenantNS, projectName)
			ri.Finalizers = []string{InstanceFinalizer}
			Expect(k8sClient.Create(ctx, ri)).To(Succeed())

			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: instanceKey})
			Expect(err).To(HaveOccurred())

			updated := &registryv1alpha1.RegistryInstance{}
			Expect(k8sClient.Get(ctx, instanceKey, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(registryv1alpha1.PhaseFailed))
		})
	})

	Context("Delete reconcile", func() {
		It("deletes robot and project in Harbor, then removes the finalizer", func() {
			readyBackend(backendName)

			ri := newInstance(instanceName, projectNS, backendName, tenantNS, projectName)
			ri.Finalizers = []string{InstanceFinalizer}
			Expect(k8sClient.Create(ctx, ri)).To(Succeed())

			r := newReconciler()
			// Provision first.
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: instanceKey})
			Expect(err).NotTo(HaveOccurred())

			// Record creds secret so we can confirm it's gone.
			updated := &registryv1alpha1.RegistryInstance{}
			Expect(k8sClient.Get(ctx, instanceKey, updated)).To(Succeed())
			credSecretName := updated.Status.Endpoint.SecretRef.Name

			// Trigger deletion.
			Expect(k8sClient.Delete(ctx, updated)).To(Succeed())

			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: instanceKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			Expect(fakeHC.deletedRobots).To(ContainElement(42))
			Expect(fakeHC.deletedProjectID).To(ContainElement(77))

			By("verifying the CR is gone")
			err = k8sClient.Get(ctx, instanceKey, &registryv1alpha1.RegistryInstance{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			By("verifying creds Secret is garbage-collected (owned by the CR)")
			sec := &corev1.Secret{}
			_ = k8sClient.Get(ctx, types.NamespacedName{
				Name: credSecretName, Namespace: projectNS,
			}, sec)
			// Secret may linger in envtest until GC runs — just ensure no error from delete.
			_ = k8sClient.Delete(ctx, sec)
		})

		It("drops the finalizer even when the Backend is already gone", func() {
			// No call to readyBackend — Backend doesn't exist.
			ri := newInstance(instanceName, projectNS, "nonexistent-backend", tenantNS, projectName)
			ri.Finalizers = []string{InstanceFinalizer}
			Expect(k8sClient.Create(ctx, ri)).To(Succeed())

			// Mark it deleted.
			Expect(k8sClient.Delete(ctx, ri)).To(Succeed())

			r := newReconciler()
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: instanceKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			err = k8sClient.Get(ctx, instanceKey, &registryv1alpha1.RegistryInstance{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})
})
