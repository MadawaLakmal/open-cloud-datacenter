//go:build e2e
// +build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/wso2/open-cloud-datacenter/operators/registry/test/utils"
)

var (
	// managerImage is the operator image built and loaded into Kind for these tests.
	// Override with IMG=... if needed.
	managerImage = "ghcr.io/wso2/registry-operator:e2e-test"

	// shouldCleanupCertManager tracks whether CertManager was installed by this suite.
	shouldCleanupCertManager = false
)

// TestE2E is the entry point for the e2e suite.
//
// Prerequisites (set via env or defaults):
//   - KIND_CLUSTER — name of the Kind cluster to use (default: registry-test-e2e)
//   - KIND        — path to the kind binary (default: kind)
//   - IMG         — override the manager image tag
//   - CERT_MANAGER_INSTALL_SKIP=true — skip cert-manager install if already present
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting registry-operator e2e test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	if img := os.Getenv("IMG"); img != "" {
		managerImage = img
	}

	By("building the manager image")
	cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", managerImage))
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the manager image")

	By("loading the manager image into Kind")
	err = utils.LoadImageToKindClusterWithName(managerImage)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the manager image into Kind")

	configureKubectlKubeRC()
	setupCertManager()
})

var _ = AfterSuite(func() {
	teardownCertManager()
})

// configureKubectlKubeRC disables kubectl kuberc by default for test isolation.
func configureKubectlKubeRC() {
	if os.Getenv("KUBECTL_KUBERC") != "true" {
		By("disabling kubectl kuberc for test isolation")
		_ = os.Setenv("KUBECTL_KUBERC", "false")
		_, _ = fmt.Fprintf(GinkgoWriter,
			"kubectl kuberc disabled (override with KUBECTL_KUBERC=true)\n")
	}
}

func setupCertManager() {
	if os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true" {
		_, _ = fmt.Fprintf(GinkgoWriter, "Skipping CertManager installation\n")
		return
	}
	By("checking if CertManager is already installed")
	if utils.IsCertManagerCRDsInstalled() {
		_, _ = fmt.Fprintf(GinkgoWriter, "CertManager already present, skipping install.\n")
		return
	}
	shouldCleanupCertManager = true
	By("installing CertManager")
	Expect(utils.InstallCertManager()).To(Succeed(), "Failed to install CertManager")
}

func teardownCertManager() {
	if !shouldCleanupCertManager {
		return
	}
	By("uninstalling CertManager")
	utils.UninstallCertManager()
}
