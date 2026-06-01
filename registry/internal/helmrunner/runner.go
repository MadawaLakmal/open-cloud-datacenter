// Package helmrunner wraps the Helm Go SDK for in-process chart install /
// upgrade / uninstall against the cluster the operator runs in. The Harbor
// chart it installs is read from a tarball vendored at /charts/harbor-<v>.tgz
// inside the container image — no runtime download from helm.goharbor.io.
//
// See docs/adr/0001-helm-vs-render.md for why Registry uses Helm while
// KeyVault and DBaaS render their workloads directly in Go.
package helmrunner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// DefaultChartsDir is the directory the Dockerfile copies the vendored chart
// tarballs into. Overridable via Runner.ChartsDir for tests.
const DefaultChartsDir = "/charts"

// Runner is the operator-wide Helm client. One instance is built at process
// start; all reconcilers share it. Concurrency-safe because each call builds
// its own action.Configuration scoped to the target namespace.
type Runner struct {
	settings  *cli.EnvSettings
	ChartsDir string
}

// New builds a Runner. The Helm settings discover the cluster the same way
// kubectl does: in-cluster service-account token first, then ~/.kube/config.
func New() *Runner {
	return &Runner{settings: cli.New(), ChartsDir: DefaultChartsDir}
}

// InstallOptions captures everything Ensure needs to install or upgrade
// a Helm release. The chart tarball is located by `<ChartsDir>/harbor-<ChartVersion>.tgz`.
type InstallOptions struct {
	ReleaseName  string
	Namespace    string
	ChartVersion string // e.g. "1.14.0"
	Values       map[string]any
	Timeout      time.Duration
}

// Ensure installs the release if absent, or upgrades it if present. Blocks
// until pods are Ready (or Timeout). Returns the resulting release.
func (r *Runner) Ensure(ctx context.Context, opts InstallOptions) (*release.Release, error) {
	log := logf.FromContext(ctx).WithName("helm")

	cfg, err := r.configure(ctx, opts.Namespace)
	if err != nil {
		return nil, err
	}

	existing, err := action.NewGet(cfg).Run(opts.ReleaseName)
	if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
		return nil, fmt.Errorf("helm get %q: %w", opts.ReleaseName, err)
	}

	chartPath := filepath.Join(r.ChartsDir, "harbor-"+opts.ChartVersion+".tgz")
	chart, err := loader.LoadFile(chartPath)
	if err != nil {
		return nil, fmt.Errorf("helm load chart %q: %w", chartPath, err)
	}

	if existing == nil {
		client := action.NewInstall(cfg)
		client.ReleaseName = opts.ReleaseName
		client.Namespace = opts.Namespace
		client.CreateNamespace = false
		client.Wait = true
		client.Timeout = opts.Timeout
		log.Info("helm install",
			"release", opts.ReleaseName,
			"namespace", opts.Namespace,
			"chart", chartPath,
			"version", opts.ChartVersion,
		)
		return client.RunWithContext(ctx, chart, opts.Values)
	}

	client := action.NewUpgrade(cfg)
	client.Namespace = opts.Namespace
	client.Wait = true
	client.Timeout = opts.Timeout
	log.Info("helm upgrade",
		"release", opts.ReleaseName,
		"namespace", opts.Namespace,
		"from", existing.Chart.Metadata.Version,
		"to", opts.ChartVersion,
	)
	return client.RunWithContext(ctx, opts.ReleaseName, chart, opts.Values)
}

// Uninstall removes a release. Already-gone is treated as success.
func (r *Runner) Uninstall(ctx context.Context, releaseName, namespace string) error {
	cfg, err := r.configure(ctx, namespace)
	if err != nil {
		return err
	}
	client := action.NewUninstall(cfg)
	client.Wait = true
	_, err = client.Run(releaseName)
	if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
		return fmt.Errorf("helm uninstall %q: %w", releaseName, err)
	}
	return nil
}

// Status returns the latest release for inspection. Returns (nil, nil) if
// no release exists.
func (r *Runner) Status(ctx context.Context, releaseName, namespace string) (*release.Release, error) {
	cfg, err := r.configure(ctx, namespace)
	if err != nil {
		return nil, err
	}
	rel, err := action.NewGet(cfg).Run(releaseName)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("helm get %q: %w", releaseName, err)
	}
	return rel, nil
}

// ---------- internals ----------

func (r *Runner) configure(ctx context.Context, namespace string) (*action.Configuration, error) {
	// Critical: Helm's RESTClientGetter reads the namespace from EnvSettings.
	// Without this call the install lands in whatever namespace the manager pod
	// runs in (e.g. registry-system) regardless of action.Install.Namespace.
	r.settings.SetNamespace(namespace)

	log := logf.FromContext(ctx).WithName("helm").V(1)
	debug := func(format string, v ...interface{}) {
		log.Info(fmt.Sprintf(format, v...))
	}

	cfg := new(action.Configuration)
	if err := cfg.Init(r.settings.RESTClientGetter(), namespace, "secret", debug); err != nil {
		return nil, fmt.Errorf("helm init for ns %q: %w", namespace, err)
	}
	return cfg, nil
}
