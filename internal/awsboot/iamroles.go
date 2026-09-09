package awsboot

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
)

const (
	clusterRoleName = "brickie-eks-cluster-role"
	nodeRoleName    = "brickie-eks-node-role"
)

var clusterRolePolicies = []string{
	"arn:aws:iam::aws:policy/AmazonEKSClusterPolicy",
}

var nodeRolePolicies = []string{
	"arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy",
	"arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy",
	"arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly",
}

func trustPolicy(service string) string {
	return fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"%s"},"Action":"sts:AssumeRole"}]}`, service)
}

func (b *Bootstrapper) ensureRole(ctx context.Context, name, service string, policies []string) (string, error) {
	out, err := b.IAM.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(name),
		AssumeRolePolicyDocument: aws.String(trustPolicy(service)),
	})
	var arn string
	var exists *iamtypes.EntityAlreadyExistsException
	switch {
	case err == nil:
		arn = aws.ToString(out.Role.Arn)
	case errors.As(err, &exists):
		got, gerr := b.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
		if gerr != nil {
			return "", fmt.Errorf("get role %s: %w", name, gerr)
		}
		arn = aws.ToString(got.Role.Arn)
	default:
		return "", fmt.Errorf("create role %s: %w", name, err)
	}
	for _, policy := range policies {
		if _, err := b.IAM.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
			RoleName:  aws.String(name),
			PolicyArn: aws.String(policy),
		}); err != nil {
			return "", fmt.Errorf("attach %s to %s: %w", policy, name, err)
		}
	}
	return arn, nil
}

func (b *Bootstrapper) EnsureClusterRole(ctx context.Context) (string, error) {
	return b.ensureRole(ctx, clusterRoleName, "eks.amazonaws.com", clusterRolePolicies)
}

func (b *Bootstrapper) EnsureNodeRole(ctx context.Context) (string, error) {
	return b.ensureRole(ctx, nodeRoleName, "ec2.amazonaws.com", nodeRolePolicies)
}
