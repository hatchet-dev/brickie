package awsboot

import (
	"context"
	"encoding/base64"
	"fmt"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func (b *Bootstrapper) BearerToken(ctx context.Context, clusterName string) (string, error) {
	presigner := sts.NewPresignClient(b.STS)
	out, err := presigner.PresignGetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}, func(o *sts.PresignOptions) {
		o.ClientOptions = append(o.ClientOptions, func(so *sts.Options) {
			so.APIOptions = append(so.APIOptions,
				smithyhttp.SetHeaderValue("x-k8s-aws-id", clusterName),
				smithyhttp.SetHeaderValue("X-Amz-Expires", "60"),
			)
		})
	})
	if err != nil {
		return "", fmt.Errorf("presign eks token: %w", err)
	}
	return "k8s-aws-v1." + base64.RawURLEncoding.EncodeToString([]byte(out.URL)), nil
}

func (b *Bootstrapper) RESTConfig(ctx context.Context, cluster *ekstypes.Cluster) (*rest.Config, error) {
	token, err := b.BearerToken(ctx, *cluster.Name)
	if err != nil {
		return nil, err
	}
	ca, err := base64.StdEncoding.DecodeString(*cluster.CertificateAuthority.Data)
	if err != nil {
		return nil, fmt.Errorf("decode cluster CA: %w", err)
	}
	return &rest.Config{
		Host:            *cluster.Endpoint,
		BearerToken:     token,
		TLSClientConfig: rest.TLSClientConfig{CAData: ca},
	}, nil
}

func (b *Bootstrapper) Clientset(ctx context.Context, cluster *ekstypes.Cluster) (*kubernetes.Clientset, error) {
	cfg, err := b.RESTConfig(ctx, cluster)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// WriteKubeconfig merges an exec-plugin entry (aws eks get-token) into the
// default kubeconfig and makes it the current context, matching what
// `aws eks update-kubeconfig` produces.
func (b *Bootstrapper) WriteKubeconfig(cluster *ekstypes.Cluster) (string, error) {
	name := *cluster.Arn
	ca, err := base64.StdEncoding.DecodeString(*cluster.CertificateAuthority.Data)
	if err != nil {
		return "", fmt.Errorf("decode cluster CA: %w", err)
	}
	pathOpts := clientcmd.NewDefaultPathOptions()
	config, err := pathOpts.GetStartingConfig()
	if err != nil {
		return "", fmt.Errorf("load kubeconfig: %w", err)
	}
	config.Clusters[name] = &clientcmdapi.Cluster{
		Server:                   *cluster.Endpoint,
		CertificateAuthorityData: ca,
	}
	config.AuthInfos[name] = &clientcmdapi.AuthInfo{
		Exec: &clientcmdapi.ExecConfig{
			APIVersion:      "client.authentication.k8s.io/v1beta1",
			Command:         "aws",
			Args:            []string{"eks", "get-token", "--cluster-name", *cluster.Name, "--region", b.Region, "--output", "json"},
			InteractiveMode: clientcmdapi.IfAvailableExecInteractiveMode,
		},
	}
	config.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name}
	config.CurrentContext = name
	if err := clientcmd.ModifyConfig(pathOpts, *config, true); err != nil {
		return "", fmt.Errorf("write kubeconfig: %w", err)
	}
	return name, nil
}
