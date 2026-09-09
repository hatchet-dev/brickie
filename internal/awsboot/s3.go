package awsboot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func (b *Bootstrapper) SnapshotBucketName() string {
	return fmt.Sprintf("brickie-snapshots-%s-%s", b.AccountID, b.Region)
}

func (b *Bootstrapper) EnsureBucket(ctx context.Context, name string) error {
	input := &s3.CreateBucketInput{Bucket: aws.String(name)}
	if b.Region != "us-east-1" {
		input.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(b.Region),
		}
	}
	_, err := b.S3.CreateBucket(ctx, input)
	var owned *s3types.BucketAlreadyOwnedByYou
	if err != nil && !errors.As(err, &owned) {
		return fmt.Errorf("create bucket %s: %w", name, err)
	}
	return nil
}

func snapshotPolicy(bucket string) string {
	return fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
{"Effect":"Allow","Action":["s3:ListBucket","s3:GetBucketLocation"],"Resource":"arn:aws:s3:::%s"},
{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject","s3:AbortMultipartUpload"],"Resource":"arn:aws:s3:::%s/*"}]}`, bucket, bucket)
}

func (b *Bootstrapper) AttachSnapshotPolicy(ctx context.Context, roleARN, bucket string) error {
	parts := strings.Split(roleARN, "/")
	roleName := parts[len(parts)-1]
	_, err := b.IAM.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String("brickie-snapshots"),
		PolicyDocument: aws.String(snapshotPolicy(bucket)),
	})
	if err != nil {
		return fmt.Errorf("attach snapshot policy to %s: %w", roleName, err)
	}
	return nil
}
