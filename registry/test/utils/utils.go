package utils

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2" //nolint:revive,staticcheck
)

const (
	certmanagerVersion = "v1.20.2"
	certmanagerURLTmpl = "https://github.com/cert-manager/cert-manager/releases/download/%s/cert-manager.yaml"

	defaultKindBinary  = "kind"
	defaultKindCluster = "kind"
)

func warnError(err error) {
	_, _ = fmt.Fprintf(GinkgoWriter, "warning: %v\n", err)
}

// Run executes cmd from the project root directory and returns combined output.
func Run(cmd *exec.Cmd) (string, error) {
	dir, _ := GetProjectDir()
	cmd.Dir = dir

	if err := os.Chdir(cmd.Dir); err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "chdir dir: %q\n", err)
	}

	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	command := strings.Join(cmd.Args, " ")
	_, _ = fmt.Fprintf(GinkgoWriter, "running: %q\n", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%q failed with error %q: %w", command, string(output), err)
	}
	return string(output), nil
}

// GetNonEmptyLines returns non-empty lines from a string.
func GetNonEmptyLines(output string) []string {
	var lines []string
	sc := bufio.NewScanner(bytes.NewBufferString(output))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// GetProjectDir returns the directory two levels above this file's package
// (test/utils → registry/).
func GetProjectDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return wd, err
	}
	// Walk up until we find go.mod.
	for dir := wd; dir != "/"; dir = parentDir(dir) {
		if _, err := os.Stat(dir + "/go.mod"); err == nil {
			return dir, nil
		}
	}
	return wd, nil
}

func parentDir(p string) string {
	idx := strings.LastIndex(p, string(os.PathSeparator))
	if idx < 0 {
		return p
	}
	return p[:idx]
}

// LoadImageToKindClusterWithName loads a Docker image into the named Kind cluster.
func LoadImageToKindClusterWithName(image string) error {
	cluster := os.Getenv("KIND_CLUSTER")
	if cluster == "" {
		cluster = defaultKindCluster
	}
	kind := os.Getenv("KIND")
	if kind == "" {
		kind = defaultKindBinary
	}
	cmd := exec.Command(kind, "load", "docker-image", image, "--name", cluster)
	_, err := Run(cmd)
	return err
}

// IsCertManagerCRDsInstalled checks whether cert-manager CRDs are present.
func IsCertManagerCRDsInstalled() bool {
	cmd := exec.Command("kubectl", "get", "crd", "certificates.cert-manager.io")
	_, err := Run(cmd)
	return err == nil
}

// InstallCertManager installs cert-manager into the cluster.
func InstallCertManager() error {
	url := fmt.Sprintf(certmanagerURLTmpl, certmanagerVersion)
	cmd := exec.Command("kubectl", "apply", "-f", url)
	if _, err := Run(cmd); err != nil {
		return err
	}
	cmd = exec.Command("kubectl", "wait", "--for=condition=Available",
		"deployment/cert-manager-webhook",
		"-n", "cert-manager",
		"--timeout=5m",
	)
	_, err := Run(cmd)
	return err
}

// UninstallCertManager removes cert-manager from the cluster.
func UninstallCertManager() {
	url := fmt.Sprintf(certmanagerURLTmpl, certmanagerVersion)
	cmd := exec.Command("kubectl", "delete", "-f", url)
	if _, err := Run(cmd); err != nil {
		warnError(err)
	}
}
