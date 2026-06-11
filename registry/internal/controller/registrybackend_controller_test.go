package controller

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"helm.sh/helm/v3/pkg/release"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/operators/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/operators/registry/internal/helmrunner"
)

// fakeHelm is a test double for HelmEnsurer. Callers can inject errors and
// inspect what was passed to Ensure/Uninstall.
type fakeHelm struct {
	ensureErr   error
	uninstallErr error

	ensureCalls   []helmrunner.InstallOptions
	uninstallCalls []string // release names
}

func (f *fakeHelm) Ensure(_ context.Context, opts helmrunner.InstallOptions) (*release.Release, error) {
	f.ensureCalls = append(f.ensureCalls, opts)
	if f.ensureErr != nil {
		return nil, f.ensureErr
	}
	return &release.Release{Name: opts.ReleaseName}, nil
}

func (f *fakeHelm) Uninstall(_ context.Context, releaseName, _ string) error {
	f.uninstallCalls = append(f.uninstallCalls, releaseName)
	return f.uninstallErr
}

var _ = Describe("RegistryBackend Controller", func() {
	const (
		backendName = "harbor"
		backendNS   = "default"
	)

	backendKey := types.NamespacedName{Name: backendName, Namespace: backendNS}

	newReconciler := func(helm HelmEnsurer) *RegistryBackendReconciler {
		return &RegistryBackendReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			Helm:   helm,
		}
	}

	newBackend := func() *registryv1alpha1.RegistryBackend {
		return &registryv1alpha1.RegistryBackend{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backendName,
				Namespace: backendNS,
			},
			Spec: registryv1alpha1.RegistryBackendSpec{
				HarborVersion: "1.14.0",
				StorageGB:     100,
			},
		}
	}

	AfterEach(func() {
		rb := &registryv1alpha1.RegistryBackend{}
		if err := k8sClient.Get(ctx, backendKey, rb); err == nil {
			rb.Finalizers = nil
			_ = k8sClient.Update(ctx, rb)
			_ = k8sClient.Delete(ctx, rb)
		}
		sec := &corev1.Secret{}
		secKey := types.NamespacedName{Name: HelmReleaseName + "-admin-credentials", Namespace: backendNS}
		if err := k8sClient.Get(ctx, secKey, sec); err == nil {
			_ = k8sClient.Delete(ctx, sec)
		}
	})

	Context("First reconcile — no existing state", func() {
		It("adds a finalizer and requeues without calling Helm", func() {
			helm := &fakeHelm{}
			r := newReconciler(helm)

			Expect(k8sClient.Create(ctx, newBackend())).To(Succeed())

			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: backendKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Requeue).To(BeTrue())
			Expect(helm.ensureCalls).To(BeEmpty(), "Helm must not be called on the finalizer-add reconcile")

			rb := &registryv1alpha1.RegistryBackend{}
			Expect(k8sClient.Get(ctx, backendKey, rb)).To(Succeed())
			Expect(rb.Finalizers).To(ContainElement(BackendFinalizer))
		})
	})

	Context("Second reconcile — finalizer present", func() {
		It("creates the admin Secret, calls Helm, and sets phase Ready", func() {
			helm := &fakeHelm{}
			r := newReconciler(helm)

			rb := newBackend()
			rb.Finalizers = []string{BackendFinalizer}
			Expect(k8sClient.Create(ctx, rb)).To(Succeed())

			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: backendKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0), "should requeue for drift detection")

			By("checking Helm was called with correct options")
			Expect(helm.ensureCalls).To(HaveLen(1))
			Expect(helm.ensureCalls[0].ReleaseName).To(Equal(HelmReleaseName))
			Expect(helm.ensureCalls[0].Namespace).To(Equal(backendNS))
			Expect(helm.ensureCalls[0].ChartVersion).To(Equal("1.14.0"))

			By("checking the admin Secret was created")
			sec := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: HelmReleaseName + "-admin-credentials", Namespace: backendNS,
			}, sec)).To(Succeed())
			Expect(sec.Data).To(HaveKey("username"))
			Expect(sec.Data).To(HaveKey("password"))
			Expect(string(sec.Data["username"])).To(Equal("admin"))
			Expect(sec.Data["password"]).NotTo(BeEmpty())

			By("checking status is Ready")
			updated := &registryv1alpha1.RegistryBackend{}
			Expect(k8sClient.Get(ctx, backendKey, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(registryv1alpha1.PhaseReady))
			Expect(updated.Status.Endpoint).NotTo(BeNil())
			Expect(updated.Status.Endpoint.Address).To(ContainSubstring("harbor-core"))
			Expect(updated.Status.Endpoint.SecretRef).NotTo(BeNil())
		})

		It("reuses the existing admin Secret on subsequent reconciles", func() {
			helm := &fakeHelm{}
			r := newReconciler(helm)

			rb := newBackend()
			rb.Finalizers = []string{BackendFinalizer}
			Expect(k8sClient.Create(ctx, rb)).To(Succeed())

			// First full reconcile — creates the secret.
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: backendKey})
			Expect(err).NotTo(HaveOccurred())

			sec := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: HelmReleaseName + "-admin-credentials", Namespace: backendNS,
			}, sec)).To(Succeed())
			firstPassword := string(sec.Data["password"])

			// Second reconcile — must not generate a new password.
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: backendKey})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: HelmReleaseName + "-admin-credentials", Namespace: backendNS,
			}, sec)).To(Succeed())
			Expect(string(sec.Data["password"])).To(Equal(firstPassword))
		})

		It("marks phase Failed when Helm returns an error", func() {
			helm := &fakeHelm{ensureErr: fmt.Errorf("helm timed out")}
			r := newReconciler(helm)

			rb := newBackend()
			rb.Finalizers = []string{BackendFinalizer}
			Expect(k8sClient.Create(ctx, rb)).To(Succeed())

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: backendKey})
			Expect(err).To(HaveOccurred())

			updated := &registryv1alpha1.RegistryBackend{}
			Expect(k8sClient.Get(ctx, backendKey, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(registryv1alpha1.PhaseFailed))
			Expect(updated.Status.Message).To(ContainSubstring("helm timed out"))
		})

		It("propagates dc-api labels to the admin Secret", func() {
			helm := &fakeHelm{}
			r := newReconciler(helm)

			rb := newBackend()
			rb.Finalizers = []string{BackendFinalizer}
			rb.Labels = map[string]string{
				LabelTenant:       "acme",
				LabelTenantUUID:   "uuid-acme",
				LabelResourceUUID: "uuid-res",
				LabelResourceKind: "Registry",
				LabelResourceName: "harbor",
			}
			Expect(k8sClient.Create(ctx, rb)).To(Succeed())

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: backendKey})
			Expect(err).NotTo(HaveOccurred())

			sec := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: HelmReleaseName + "-admin-credentials", Namespace: backendNS,
			}, sec)).To(Succeed())
			Expect(sec.Labels[LabelTenant]).To(Equal("acme"))
			Expect(sec.Labels[LabelTenantUUID]).To(Equal("uuid-acme"))
		})
	})

	Context("Delete reconcile", func() {
		It("calls Helm uninstall and removes the finalizer", func() {
			helm := &fakeHelm{}
			r := newReconciler(helm)

			rb := newBackend()
			rb.Finalizers = []string{BackendFinalizer}
			Expect(k8sClient.Create(ctx, rb)).To(Succeed())

			By("triggering deletion")
			Expect(k8sClient.Delete(ctx, rb)).To(Succeed())

			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: backendKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			Expect(helm.uninstallCalls).To(ContainElement(HelmReleaseName))

			By("verifying the CR is gone (finalizer removed)")
			updated := &registryv1alpha1.RegistryBackend{}
			err = k8sClient.Get(ctx, backendKey, updated)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("Trivy addon toggle", func() {
		It("passes trivy.enabled=true to Helm values when addon is enabled", func() {
			helm := &fakeHelm{}
			r := newReconciler(helm)

			rb := newBackend()
			rb.Finalizers = []string{BackendFinalizer}
			rb.Spec.EngineConfig.Addons.Trivy.Enabled = true
			Expect(k8sClient.Create(ctx, rb)).To(Succeed())

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: backendKey})
			Expect(err).NotTo(HaveOccurred())

			Expect(helm.ensureCalls).To(HaveLen(1))
			trivyRaw, ok := helm.ensureCalls[0].Values["trivy"]
			Expect(ok).To(BeTrue(), "trivy key must be in Helm values")
			trivyMap, ok := trivyRaw.(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(trivyMap["enabled"]).To(BeTrue())
		})
	})
})
