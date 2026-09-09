package awsboot

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
)

const nodeLaunchTemplate = "brickie-node"

// ensureLaunchTemplate exists to raise the IMDS hop limit to 2: EKS nodes
// default to 1, which leaves pods (the CSI node's S3 offload, the provider)
// without instance-role credentials.
func (b *Bootstrapper) ensureLaunchTemplate(ctx context.Context) (string, error) {
	out, err := b.EC2.CreateLaunchTemplate(ctx, &ec2.CreateLaunchTemplateInput{
		LaunchTemplateName: aws.String(nodeLaunchTemplate),
		LaunchTemplateData: &ec2types.RequestLaunchTemplateData{
			MetadataOptions: &ec2types.LaunchTemplateInstanceMetadataOptionsRequest{
				HttpTokens:              ec2types.LaunchTemplateHttpTokensStateRequired,
				HttpPutResponseHopLimit: aws.Int32(2),
			},
		},
	})
	if err == nil {
		return aws.ToString(out.LaunchTemplate.LaunchTemplateId), nil
	}
	desc, derr := b.EC2.DescribeLaunchTemplates(ctx, &ec2.DescribeLaunchTemplatesInput{
		LaunchTemplateNames: []string{nodeLaunchTemplate},
	})
	if derr != nil || len(desc.LaunchTemplates) == 0 {
		return "", fmt.Errorf("create launch template: %w", err)
	}
	return aws.ToString(desc.LaunchTemplates[0].LaunchTemplateId), nil
}

var gravitonFamily = regexp.MustCompile(`^[a-z]+[0-9]+[a-z]*g`)

func IsGraviton(instanceType string) bool {
	family, _, _ := strings.Cut(instanceType, ".")
	return gravitonFamily.MatchString(family)
}

func (b *Bootstrapper) CreateCluster(ctx context.Context, name, roleARN string, subnetIDs []string) error {
	input := &eks.CreateClusterInput{
		Name:    aws.String(name),
		RoleArn: aws.String(roleARN),
		ResourcesVpcConfig: &ekstypes.VpcConfigRequest{
			SubnetIds: subnetIDs,
		},
		AccessConfig: &ekstypes.CreateAccessConfigRequest{
			AuthenticationMode: ekstypes.AuthenticationModeApiAndConfigMap,
		},
	}
	deadline := time.Now().Add(45 * time.Second)
	for {
		_, err := b.EKS.CreateCluster(ctx, input)
		if err == nil {
			return nil
		}
		if _, ok := errors.AsType[*ekstypes.ResourceInUseException](err); ok {
			return nil
		}
		var invalid *ekstypes.InvalidParameterException
		if errors.As(err, &invalid) && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
			continue
		}
		return fmt.Errorf("create cluster %s: %w", name, err)
	}
}

func (b *Bootstrapper) WaitClusterActive(ctx context.Context, name string) error {
	waiter := eks.NewClusterActiveWaiter(b.EKS)
	return waiter.Wait(ctx, &eks.DescribeClusterInput{Name: aws.String(name)}, 30*time.Minute)
}

func (b *Bootstrapper) CreateNodegroup(ctx context.Context, cluster, nodeRoleARN, instanceType string, nodeCount int32, subnetIDs []string) error {
	amiType := ekstypes.AMITypesAl2023X8664Standard
	if IsGraviton(instanceType) {
		amiType = ekstypes.AMITypesAl2023Arm64Standard
	}
	ltID, err := b.ensureLaunchTemplate(ctx)
	if err != nil {
		return err
	}
	_, err = b.EKS.CreateNodegroup(ctx, &eks.CreateNodegroupInput{
		ClusterName:    aws.String(cluster),
		NodegroupName:  aws.String("brickie-default"),
		NodeRole:       aws.String(nodeRoleARN),
		Subnets:        subnetIDs,
		InstanceTypes:  []string{instanceType},
		AmiType:        amiType,
		LaunchTemplate: &ekstypes.LaunchTemplateSpecification{Id: aws.String(ltID)},
		ScalingConfig: &ekstypes.NodegroupScalingConfig{
			MinSize:     aws.Int32(1),
			MaxSize:     aws.Int32(max(nodeCount, 1)),
			DesiredSize: aws.Int32(nodeCount),
		},
	})
	if _, ok := errors.AsType[*ekstypes.ResourceInUseException](err); ok {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create nodegroup: %w", err)
	}
	return nil
}

func (b *Bootstrapper) WaitNodegroupActive(ctx context.Context, cluster string) error {
	waiter := eks.NewNodegroupActiveWaiter(b.EKS)
	return waiter.Wait(ctx, &eks.DescribeNodegroupInput{
		ClusterName:   aws.String(cluster),
		NodegroupName: aws.String("brickie-default"),
	}, 20*time.Minute)
}

func (b *Bootstrapper) DescribeCluster(ctx context.Context, name string) (*ekstypes.Cluster, error) {
	out, err := b.EKS.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(name)})
	if err != nil {
		return nil, fmt.Errorf("describe cluster %s: %w", name, err)
	}
	return out.Cluster, nil
}

func (b *Bootstrapper) ListClusters(ctx context.Context) ([]string, error) {
	names := []string{}
	var next *string
	for {
		out, err := b.EKS.ListClusters(ctx, &eks.ListClustersInput{NextToken: next})
		if err != nil {
			return nil, fmt.Errorf("list clusters: %w", err)
		}
		names = append(names, out.Clusters...)
		if out.NextToken == nil {
			return names, nil
		}
		next = out.NextToken
	}
}

func (b *Bootstrapper) NodegroupRoles(ctx context.Context, cluster string) ([]string, error) {
	groups, err := b.EKS.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: aws.String(cluster)})
	if err != nil {
		return nil, fmt.Errorf("list nodegroups: %w", err)
	}
	seen := map[string]bool{}
	roles := []string{}
	for _, ng := range groups.Nodegroups {
		out, err := b.EKS.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(cluster),
			NodegroupName: aws.String(ng),
		})
		if err != nil {
			return nil, fmt.Errorf("describe nodegroup %s: %w", ng, err)
		}
		arn := aws.ToString(out.Nodegroup.NodeRole)
		if arn != "" && !seen[arn] {
			seen[arn] = true
			roles = append(roles, arn)
		}
	}
	return roles, nil
}

func (b *Bootstrapper) EnsureAccess(ctx context.Context, cluster string) error {
	principal := b.PrincipalARN()
	_, err := b.EKS.CreateAccessEntry(ctx, &eks.CreateAccessEntryInput{
		ClusterName:  aws.String(cluster),
		PrincipalArn: aws.String(principal),
	})
	var inUse *ekstypes.ResourceInUseException
	if err != nil && !errors.As(err, &inUse) {
		return fmt.Errorf("create access entry for %s: %w", principal, err)
	}
	_, err = b.EKS.AssociateAccessPolicy(ctx, &eks.AssociateAccessPolicyInput{
		ClusterName:  aws.String(cluster),
		PrincipalArn: aws.String(principal),
		PolicyArn:    aws.String("arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"),
		AccessScope:  &ekstypes.AccessScope{Type: ekstypes.AccessScopeTypeCluster},
	})
	if err != nil {
		return fmt.Errorf("associate cluster admin policy for %s: %w", principal, err)
	}
	return nil
}
