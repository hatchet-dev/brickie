package awsboot

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
)

func (b *Bootstrapper) EnsureRepo(ctx context.Context, name string) (string, error) {
	out, err := b.ECR.CreateRepository(ctx, &ecr.CreateRepositoryInput{RepositoryName: aws.String(name)})
	if err == nil {
		return aws.ToString(out.Repository.RepositoryUri), nil
	}
	if _, ok := errors.AsType[*ecrtypes.RepositoryAlreadyExistsException](err); !ok {
		return "", fmt.Errorf("create ecr repository %s: %w", name, err)
	}
	desc, err := b.ECR.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{RepositoryNames: []string{name}})
	if err != nil || len(desc.Repositories) == 0 {
		return "", fmt.Errorf("describe ecr repository %s: %w", name, err)
	}
	return aws.ToString(desc.Repositories[0].RepositoryUri), nil
}

func (b *Bootstrapper) RegistryLogin(ctx context.Context) (registry, user, password string, err error) {
	out, err := b.ECR.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil || len(out.AuthorizationData) == 0 {
		return "", "", "", fmt.Errorf("ecr authorization token: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(aws.ToString(out.AuthorizationData[0].AuthorizationToken))
	if err != nil {
		return "", "", "", fmt.Errorf("decode ecr token: %w", err)
	}
	user, password, found := strings.Cut(string(raw), ":")
	if !found {
		return "", "", "", fmt.Errorf("unexpected ecr token format")
	}
	registry = strings.TrimPrefix(aws.ToString(out.AuthorizationData[0].ProxyEndpoint), "https://")
	return registry, user, password, nil
}
