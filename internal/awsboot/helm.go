package awsboot

import (
	"context"
	"errors"
	"fmt"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	helmdriver "helm.sh/helm/v3/pkg/storage/driver"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

type restClientGetter struct {
	cfg *rest.Config
}

func (g *restClientGetter) ToRESTConfig() (*rest.Config, error) {
	return g.cfg, nil
}

func (g *restClientGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(g.cfg)
	if err != nil {
		return nil, err
	}
	return memory.NewMemCacheClient(dc), nil
}

func (g *restClientGetter) ToRESTMapper() (meta.RESTMapper, error) {
	dc, err := g.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	return restmapper.NewDeferredDiscoveryRESTMapper(dc), nil
}

func (g *restClientGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return clientcmd.NewDefaultClientConfig(*clientcmdapi.NewConfig(), &clientcmd.ConfigOverrides{})
}

func helmConfig(cfg *rest.Config, namespace string) (*action.Configuration, error) {
	actionCfg := new(action.Configuration)
	if err := actionCfg.Init(&restClientGetter{cfg: cfg}, namespace, "secret", func(string, ...any) {}); err != nil {
		return nil, fmt.Errorf("init helm: %w", err)
	}
	rc, err := registry.NewClient()
	if err != nil {
		return nil, fmt.Errorf("init helm registry client: %w", err)
	}
	actionCfg.RegistryClient = rc
	return actionCfg, nil
}

func ReleaseExists(cfg *rest.Config, namespace, release string) (bool, error) {
	actionCfg, err := helmConfig(cfg, namespace)
	if err != nil {
		return false, err
	}
	history := action.NewHistory(actionCfg)
	history.Max = 1
	_, err = history.Run(release)
	if errors.Is(err, helmdriver.ErrReleaseNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func loadChart(actionCfg *action.Configuration, chartRef, version string) (*chart.Chart, error) {
	if !registry.IsOCI(chartRef) {
		return loader.Load(chartRef)
	}
	cpo := action.NewInstall(actionCfg).ChartPathOptions
	cpo.Version = version
	path, err := cpo.LocateChart(chartRef, cli.New())
	if err != nil {
		return nil, fmt.Errorf("pull chart %s: %w", chartRef, err)
	}
	return loader.Load(path)
}

// InstallChart installs or upgrades a release from a local chart path or an
// oci:// reference (version applies to oci:// only).
func InstallChart(ctx context.Context, cfg *rest.Config, namespace, release, chartRef, version string, values map[string]any) error {
	actionCfg, err := helmConfig(cfg, namespace)
	if err != nil {
		return err
	}
	chart, err := loadChart(actionCfg, chartRef, version)
	if err != nil {
		return fmt.Errorf("load chart %s: %w", chartRef, err)
	}
	exists, err := ReleaseExists(cfg, namespace, release)
	if err != nil {
		return err
	}
	if !exists {
		install := action.NewInstall(actionCfg)
		install.ReleaseName = release
		install.Namespace = namespace
		install.CreateNamespace = true
		install.Timeout = 5 * time.Minute
		_, err = install.RunWithContext(ctx, chart, values)
		if err != nil {
			return fmt.Errorf("helm install %s: %w", release, err)
		}
		return nil
	}
	upgrade := action.NewUpgrade(actionCfg)
	upgrade.Namespace = namespace
	upgrade.Timeout = 5 * time.Minute
	if _, err := upgrade.RunWithContext(ctx, release, chart, values); err != nil {
		return fmt.Errorf("helm upgrade %s: %w", release, err)
	}
	return nil
}
