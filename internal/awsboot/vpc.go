package awsboot

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func (b *Bootstrapper) DefaultSubnets(ctx context.Context) ([]string, error) {
	vpcs, err := b.EC2.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{{Name: aws.String("is-default"), Values: []string{"true"}}},
	})
	if err != nil {
		return nil, fmt.Errorf("describe vpcs: %w", err)
	}
	if len(vpcs.Vpcs) == 0 {
		return nil, fmt.Errorf("this account/region has no default VPC: create one with `aws ec2 create-default-vpc` or pass --subnet-ids")
	}
	vpcID := aws.ToString(vpcs.Vpcs[0].VpcId)
	subnets, err := b.EC2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{vpcID}}},
	})
	if err != nil {
		return nil, fmt.Errorf("describe subnets: %w", err)
	}
	azs := map[string]bool{}
	ids := []string{}
	for _, s := range subnets.Subnets {
		azs[aws.ToString(s.AvailabilityZone)] = true
		ids = append(ids, aws.ToString(s.SubnetId))
	}
	if len(azs) < 2 {
		return nil, fmt.Errorf("default VPC %s spans %d availability zone(s); EKS needs at least 2 (pass --subnet-ids)", vpcID, len(azs))
	}
	return ids, nil
}
