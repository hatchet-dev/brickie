// Package awsboot provisions EKS clusters and installs brickie on them.
package awsboot

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type Bootstrapper struct {
	Region    string
	AccountID string
	CallerARN string
	EKS       *eks.Client
	EC2       *ec2.Client
	IAM       *iam.Client
	ECR       *ecr.Client
	S3        *s3.Client
	STS       *sts.Client
}

func loadOpts(region, profile string) []func(*config.LoadOptions) error {
	opts := []func(*config.LoadOptions) error{}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	return opts
}

func New(ctx context.Context, region, profile string) (*Bootstrapper, error) {
	cfg, err := config.LoadDefaultConfig(ctx, loadOpts(region, profile)...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	stsc := sts.NewFromConfig(cfg)
	id, err := stsc.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("aws credentials are not configured (aws configure or environment variables): %w", err)
	}
	return &Bootstrapper{
		Region:    region,
		AccountID: aws.ToString(id.Account),
		CallerARN: aws.ToString(id.Arn),
		EKS:       eks.NewFromConfig(cfg),
		EC2:       ec2.NewFromConfig(cfg),
		IAM:       iam.NewFromConfig(cfg),
		ECR:       ecr.NewFromConfig(cfg),
		S3:        s3.NewFromConfig(cfg),
		STS:       stsc,
	}, nil
}

func DefaultRegion(ctx context.Context, profile string) string {
	cfg, err := config.LoadDefaultConfig(ctx, loadOpts("", profile)...)
	if err != nil {
		return ""
	}
	return cfg.Region
}

var profileSection = regexp.MustCompile(`^\[\s*(?:profile\s+)?([^\]]+?)\s*\]`)

// ListProfiles reads profile names from the shared AWS config and credentials
// files. Errors collapse to an empty list: profile selection is a convenience,
// and the credential chain reports real problems later.
func ListProfiles() []string {
	seen := map[string]bool{}
	names := []string{}
	for _, path := range []string{config.DefaultSharedConfigFilename(), config.DefaultSharedCredentialsFilename()} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for line := range strings.Lines(string(raw)) {
			if m := profileSection.FindStringSubmatch(strings.TrimSpace(line)); m != nil && !seen[m[1]] {
				seen[m[1]] = true
				names = append(names, m[1])
			}
		}
	}
	sort.Strings(names)
	return names
}

// PrincipalARN normalizes an assumed-role STS caller ARN to the IAM role ARN
// that EKS access entries expect.
func (b *Bootstrapper) PrincipalARN() string {
	arn := b.CallerARN
	if strings.Contains(arn, ":assumed-role/") {
		parts := strings.SplitN(arn, "/", 3)
		if len(parts) >= 2 {
			account := strings.Split(arn, ":")[4]
			return fmt.Sprintf("arn:aws:iam::%s:role/%s", account, parts[1])
		}
	}
	return arn
}
