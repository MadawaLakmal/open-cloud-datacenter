//go:build e2e
// +build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/wso2/open-cloud-datacenter/operators/registry/test/utils"
)

// operatorNamespace is the namespace the registry-operator manager runs in.
const operatorNamespace = "registry-system"

var _ = Describe("Registry Operator", Ordered, func() {
	var controllerPodName string

	BeforeAll(func() {
		By("creating the operator namespace")
		cmd := exec.Command("kubectl", "create", "ns", operatorNamespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("labeling namespace with restricted pod-security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", operatorNamespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		By("cleaning up metrics curl pod")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", operatorNamespace,
			"--ignore-not-found")
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing operator namespace")
		cmd = exec.Command("kubectl", "delete", "ns", operatorNamespace)
		_, _ = utils.Run(cmd)
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			By("fetching controller logs for debugging")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", operatorNamespace)
			if out, err := utils.Run(cmd); err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n%s", out)
			}

			By("fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", operatorNamespace,
				"--sort-by=.lastTimestamp")
			if out, err := utils.Run(cmd); err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Events:\n%s", out)
			}
		}
	})

	SetDefaultEventuallyTimeout(3 * time.Minute)
	SetDefaultEventuallyPollingInterval(5 * time.Second)

	Context("Manager pod", func() {
		It("starts successfully and reaches Running", func() {
			verifyUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods",
					"-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", operatorNamespace,
				)
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				names := utils.GetNonEmptyLines(out)
				g.Expect(names).To(HaveLen(1))
				controllerPodName = names[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				cmd = exec.Command("kubectl", "get", "pods", controllerPodName,
					"-o", "jsonpath={.status.phase}", "-n", operatorNamespace)
				phase, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(phase).To(Equal("Running"))
			}
			Eventually(verifyUp).Should(Succeed())
		})
	})

	Context("Metrics endpoint", func() {
		const (
			serviceAccountName  = "registry-operator-controller-manager"
			metricsServiceName  = "registry-operator-controller-manager-metrics-service"
			metricsBindingName  = "registry-operator-metrics-binding"
		)

		It("serves metrics over HTTPS", func() {
			By("creating ClusterRoleBinding for metrics access")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsBindingName,
				"--clusterrole=registry-operator-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", operatorNamespace, serviceAccountName),
			)
			_, _ = utils.Run(cmd) // ignore if already exists

			By("waiting for metrics service to exist")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", operatorNamespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the controller manager serves the metrics server")
			verifyMetrics := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", operatorNamespace)
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring("Serving metrics server"))
			}
			Eventually(verifyMetrics, 3*time.Minute, time.Second).Should(Succeed())
		})
	})

	// ── RegistryBackend smoke test ────────────────────────────────────────────
	// NOTE: This test requires Harbor to actually deploy, so it only runs when
	// HARBOR_E2E=true is set. On a Kind cluster Harbor pods may not have the
	// required storage class; use a harvester-dev cluster for the full flow.
	// See docs/test-environments.md for the recommended setup.
	Context("RegistryBackend CR", func() {
		const (
			tenantNS   = "dc-tenant-e2e"
			backendCR  = "harbor"
		)

		BeforeAll(func() {
			cmd := exec.Command("kubectl", "create", "ns", tenantNS)
			_, _ = utils.Run(cmd)
		})

		AfterAll(func() {
			cmd := exec.Command("kubectl", "delete", "ns", tenantNS, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("progresses to Provisioning phase after creation", func() {
			// Apply the sample RegistryBackend CR.
			cmd := exec.Command("kubectl", "apply",
				"-f", "config/samples/registry_v1alpha1_registrybackend.yaml",
				"-n", tenantNS,
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			// The operator should at least set phase=Provisioning within one reconcile.
			verifyProvisioning := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "registrybackend", backendCR,
					"-n", tenantNS,
					"-o", "jsonpath={.status.phase}",
				)
				phase, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(phase)).NotTo(BeEmpty(),
					"phase should be set within the first reconcile")
			}
			Eventually(verifyProvisioning, time.Minute).Should(Succeed())

			By("cleaning up the RegistryBackend")
			cmd = exec.Command("kubectl", "delete", "registrybackend", backendCR,
				"-n", tenantNS, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})
	})
})
