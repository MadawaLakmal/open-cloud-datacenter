//go:build e2e
// +build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/wso2/open-cloud-datacenter/operators/registry/test/utils"
)

// operatorNamespace is the namespace the registry-operator manager runs in.
const operatorNamespace = "registry-system"

// serviceAccountName is the ServiceAccount created for the controller manager.
const serviceAccountName = "registry-operator-controller-manager"

// metricsServiceName is the name of the metrics Service of the controller manager.
const metricsServiceName = "registry-operator-controller-manager-metrics-service"

// metricsRoleBindingName is the ClusterRoleBinding created to allow reading metrics.
const metricsRoleBindingName = "registry-operator-metrics-binding"

var _ = Describe("Registry Operator", Ordered, func() {
	var controllerPodName string

	BeforeAll(func() {
		By("creating the operator namespace")
		cmd := exec.Command("kubectl", "create", "ns", operatorNamespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling namespace with restricted pod-security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", operatorNamespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", operatorNamespace,
			"--ignore-not-found")
		_, _ = utils.Run(cmd)

		By("cleaning up the metrics ClusterRoleBinding")
		cmd = exec.Command("kubectl", "delete", "clusterrolebinding", metricsRoleBindingName,
			"--ignore-not-found")
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing operator namespace")
		cmd = exec.Command("kubectl", "delete", "ns", operatorNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			By("fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", operatorNamespace)
			if out, err := utils.Run(cmd); err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n%s", out)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get controller logs: %s\n", err)
			}

			By("fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", operatorNamespace,
				"--sort-by=.lastTimestamp")
			if out, err := utils.Run(cmd); err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Events:\n%s", out)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get events: %s\n", err)
			}

			By("fetching curl-metrics pod logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", operatorNamespace)
			if out, err := utils.Run(cmd); err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics curl logs:\n%s", out)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s\n", err)
			}

			By("fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", operatorNamespace)
			if out, err := utils.Run(cmd); err == nil {
				fmt.Println("Pod description:\n", out)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(3 * time.Minute)
	SetDefaultEventuallyPollingInterval(5 * time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", operatorNamespace,
				)
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				names := utils.GetNonEmptyLines(out)
				g.Expect(names).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = names[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", operatorNamespace,
				)
				phase, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(phase).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=registry-operator-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", operatorNamespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", operatorNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("ensuring the controller pod is ready")
			verifyControllerPodReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", operatorNamespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}
			Eventually(verifyControllerPodReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", operatorNamespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted, 3*time.Minute, time.Second).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", operatorNamespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": [
								"for i in $(seq 1 30); do curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics && exit 0 || sleep 2; done; exit 1"
							],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, operatorNamespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", operatorNamespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			verifyMetricsAvailable := func(g Gomega) {
				metricsOutput, err := getMetricsOutput()
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
				g.Expect(metricsOutput).NotTo(BeEmpty())
				g.Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
			}
			Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
		})
	})

	// ── RegistryBackend lifecycle ─────────────────────────────────────────────
	// Requires a cluster with working PVC storage (e.g. Longhorn on Harvester).
	// Set HARBOR_E2E=true to opt in; skipped otherwise so Kind CI stays green.
	// See docs/test-environments.md for the recommended setup.
	Context("RegistryBackend CR", func() {
		const (
			tenantNS  = "dc-tenant-e2e"
			backendCR = "harbor"
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
			if os.Getenv("HARBOR_E2E") != "true" {
				Skip("set HARBOR_E2E=true to run Harbor lifecycle tests")
			}

			cmd := exec.Command("kubectl", "apply",
				"-f", "config/samples/registry_v1alpha1_registrybackend.yaml",
				"-n", tenantNS,
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

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

	// ── RegistryInstance lifecycle ────────────────────────────────────────────
	// Requires a fully-ready Harbor cluster (RegistryBackend phase=Ready).
	// Set HARBOR_E2E=true; skipped otherwise.
	Context("RegistryInstance CR", func() {
		const (
			instanceNS        = "dc-e2e-billing"
			instanceCR        = "billing"
			instanceBackendNS = "dc-tenant-e2e"
			instanceBackendCR = "harbor"
		)

		BeforeAll(func() {
			if os.Getenv("HARBOR_E2E") != "true" {
				return
			}
			cmd := exec.Command("kubectl", "create", "ns", instanceNS)
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "create", "ns", instanceBackendNS)
			_, _ = utils.Run(cmd)
		})

		AfterAll(func() {
			if os.Getenv("HARBOR_E2E") != "true" {
				return
			}
			cmd := exec.Command("kubectl", "delete", "ns", instanceNS, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", instanceBackendNS, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("reaches Ready phase, exposes a robot-credentials Secret, and cleans up on delete", func() {
			if os.Getenv("HARBOR_E2E") != "true" {
				Skip("set HARBOR_E2E=true to run Harbor lifecycle tests")
			}

			By("provisioning the RegistryBackend and waiting for Ready")
			cmd := exec.Command("kubectl", "apply",
				"-f", "config/samples/registry_v1alpha1_registrybackend.yaml",
				"-n", instanceBackendNS,
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			verifyBackendReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "registrybackend", instanceBackendCR,
					"-n", instanceBackendNS, "-o", "jsonpath={.status.phase}")
				phase, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(phase)).To(Equal("Ready"))
			}
			Eventually(verifyBackendReady, 15*time.Minute, 10*time.Second).Should(Succeed())

			By("creating a RegistryInstance")
			instanceYAML := fmt.Sprintf(`apiVersion: registry.opencloud.wso2.com/v1alpha1
kind: RegistryInstance
metadata:
  name: %s
  namespace: %s
spec:
  backendRef:
    name: %s
    namespace: %s
  projectName: billing
`, instanceCR, instanceNS, instanceBackendCR, instanceBackendNS)
			tmpFile := "/tmp/registry-e2e-instance.yaml"
			Expect(os.WriteFile(tmpFile, []byte(instanceYAML), 0o644)).To(Succeed())
			cmd = exec.Command("kubectl", "apply", "-f", tmpFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for RegistryInstance to reach Ready")
			verifyInstanceReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "registryinstance", instanceCR,
					"-n", instanceNS, "-o", "jsonpath={.status.phase}")
				phase, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(phase)).To(Equal("Ready"))
			}
			Eventually(verifyInstanceReady, 3*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying the robot-credentials Secret exists")
			var credSecretName string
			verifySecret := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "registryinstance", instanceCR,
					"-n", instanceNS, "-o", "jsonpath={.status.endpoint.secretRef.name}")
				name, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(name)).NotTo(BeEmpty())
				credSecretName = strings.TrimSpace(name)

				cmd = exec.Command("kubectl", "get", "secret", credSecretName, "-n", instanceNS)
				_, err = utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifySecret).Should(Succeed())

			By("deleting the RegistryInstance and verifying the CR is removed")
			cmd = exec.Command("kubectl", "delete", "registryinstance", instanceCR,
				"-n", instanceNS)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			verifyGone := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "registryinstance", instanceCR,
					"-n", instanceNS)
				_, err := utils.Run(cmd)
				g.Expect(err).To(HaveOccurred(), "RegistryInstance CR should be gone after deletion")
			}
			Eventually(verifyGone, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("cleaning up the RegistryBackend")
			cmd = exec.Command("kubectl", "delete", "registrybackend", instanceBackendCR,
				"-n", instanceBackendNS, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})
	})
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	By("creating temporary file to store the token request")
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		By("executing kubectl command to create the token")
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			operatorNamespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		By("parsing the JSON output to extract the token")
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", operatorNamespace)
	return utils.Run(cmd)
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
