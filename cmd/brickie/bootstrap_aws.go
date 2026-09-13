package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"
	"github.com/charmbracelet/lipgloss"
	charmlog "github.com/charmbracelet/log"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/hatchet-dev/brickie/internal/awsboot"
)

var clusterNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-]{0,99}$`)

type awsBootFlags struct {
	profile         string
	region          string
	existingCluster string
	clusterName     string
	instanceType    string
	nodeCount       int
	subnetIDs       []string
	namespace       string
	release         string
	chart           string
	imageTag        string
	imagePlatform   string
	token           string
	anthropic       string
	claudeOAuth     string
	sandboxImg      string
}

func bootstrapAWSCmd() *cobra.Command {
	f := awsBootFlags{}
	cmd := &cobra.Command{
		Use:   "bootstrap-aws",
		Short: "Create or reuse an EKS cluster and deploy brickie with gVisor sandboxes, ECR, and S3 snapshots",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBootstrapAWS(cmd, f)
		},
	}
	cmd.Flags().StringVar(&f.profile, "profile", "", "AWS profile (default $AWS_PROFILE or the default profile)")
	cmd.Flags().StringVar(&f.region, "region", "", "AWS region")
	cmd.Flags().StringVar(&f.existingCluster, "existing-cluster", "", "use this existing EKS cluster instead of creating one")
	cmd.Flags().StringVar(&f.clusterName, "cluster-name", "", "name for a new EKS cluster")
	cmd.Flags().StringVar(&f.instanceType, "instance-type", "m5.large", "node instance type for a new cluster")
	cmd.Flags().IntVar(&f.nodeCount, "node-count", 2, "node count for a new cluster")
	cmd.Flags().StringSliceVar(&f.subnetIDs, "subnet-ids", nil, "subnet ids for a new cluster (default: the default VPC's subnets)")
	cmd.Flags().StringVarP(&f.namespace, "namespace", "n", envOr("NAMESPACE", "brickie"), "namespace for the provider and sandboxes")
	cmd.Flags().StringVar(&f.release, "release", "brickie", "helm release name")
	cmd.Flags().StringVar(&f.chart, "chart", "helm/brickie", "path to the brickie chart")
	cmd.Flags().StringVar(&f.imageTag, "image-tag", "latest", "tag for the provider image pushed to ECR")
	cmd.Flags().StringVar(&f.imagePlatform, "image-platform", "", "docker build platform (default: matches the node architecture)")
	cmd.Flags().StringVar(&f.token, "hatchet-token", "", "Hatchet client token (default $HATCHET_CLIENT_TOKEN)")
	cmd.Flags().StringVar(&f.anthropic, "anthropic-api-key", "", "inject ANTHROPIC_API_KEY into sandboxes (default $ANTHROPIC_API_KEY)")
	cmd.Flags().StringVar(&f.claudeOAuth, "claude-oauth-token", "", "inject CLAUDE_CODE_OAUTH_TOKEN into sandboxes (default $CLAUDE_CODE_OAUTH_TOKEN)")
	cmd.Flags().StringVar(&f.sandboxImg, "sandbox-image", "", "default sandbox image (chart default: alpine:3)")
	return cmd
}

func interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func step(ctx context.Context, title string, fn func(ctx context.Context) error) error {
	if !interactive() {
		say("%s", title)
		return fn(ctx)
	}
	var fnErr error
	spinErr := spinner.New().Title(title).ActionWithErr(func(sctx context.Context) error {
		fnErr = fn(sctx)
		return fnErr
	}).Run()
	if fnErr != nil {
		return fnErr
	}
	return spinErr
}

func askOne(field huh.Field) error {
	return huh.NewForm(huh.NewGroup(field)).Run()
}

func runBootstrapAWS(cmd *cobra.Command, f awsBootFlags) error {
	ctx := cmd.Context()
	logger := charmlog.New(os.Stderr)

	if f.token == "" {
		f.token = os.Getenv("HATCHET_CLIENT_TOKEN")
	}
	if f.anthropic == "" {
		f.anthropic = os.Getenv("ANTHROPIC_API_KEY")
	}
	if f.claudeOAuth == "" {
		f.claudeOAuth = os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	}
	if f.token == "" {
		return errors.New("HATCHET_CLIENT_TOKEN is required: export it or pass --hatchet-token")
	}
	if !have("docker") {
		return errors.New("docker is required")
	}
	if _, err := output("docker", "info"); err != nil {
		return errors.New("start Docker first")
	}

	if f.profile == "" && interactive() {
		if profiles := awsboot.ListProfiles(); len(profiles) > 1 {
			f.profile = os.Getenv("AWS_PROFILE")
			if f.profile == "" {
				f.profile = profiles[0]
			}
			if err := askOne(huh.NewSelect[string]().Title("AWS profile").Options(huh.NewOptions(profiles...)...).Value(&f.profile)); err != nil {
				return err
			}
		}
	}
	if !cmd.Flags().Changed("region") {
		f.region = awsboot.DefaultRegion(ctx, f.profile)
	}
	if f.region == "" {
		if !interactive() {
			return errors.New("--region is required (no region in the AWS config chain)")
		}
		if err := askOne(huh.NewInput().Title("AWS region").Placeholder("us-east-1").Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("region is required")
			}
			return nil
		}).Value(&f.region)); err != nil {
			return err
		}
	}

	b, err := awsboot.New(ctx, f.region, f.profile)
	if err != nil {
		return err
	}
	profileName := f.profile
	if profileName == "" {
		profileName = envOr("AWS_PROFILE", "default")
	}
	logger.Info("aws ready", "account", b.AccountID, "region", b.Region, "profile", profileName)

	create := false
	switch {
	case f.existingCluster != "" && f.clusterName != "":
		return errors.New("pass either --existing-cluster or --cluster-name, not both")
	case f.existingCluster != "":
	case f.clusterName != "":
		create = true
	default:
		if !interactive() {
			return errors.New("pass --cluster-name (create) or --existing-cluster (reuse)")
		}
		mode := "create"
		if err := askOne(huh.NewSelect[string]().Title("EKS cluster").Options(
			huh.NewOption("Create a new EKS cluster", "create"),
			huh.NewOption("Use an existing cluster", "existing"),
		).Value(&mode)); err != nil {
			return err
		}
		if mode == "create" {
			create = true
			f.clusterName = "brickie"
			if err := askOne(huh.NewInput().Title("Cluster name").Validate(func(s string) error {
				if !clusterNameRe.MatchString(s) {
					return errors.New("letters, digits, and hyphens; must start with a letter")
				}
				return nil
			}).Value(&f.clusterName)); err != nil {
				return err
			}
			if err := askOne(huh.NewSelect[string]().Title("Node instance type").Options(
				huh.NewOptions("m5.large", "m5.xlarge", "c5.xlarge", "t3.xlarge", "m7g.large", "m7g.xlarge")...,
			).Value(&f.instanceType)); err != nil {
				return err
			}
			count := strconv.Itoa(f.nodeCount)
			if err := askOne(huh.NewInput().Title("Node count").Validate(func(s string) error {
				n, err := strconv.Atoi(s)
				if err != nil || n < 1 || n > 20 {
					return errors.New("enter a number between 1 and 20")
				}
				return nil
			}).Value(&count)); err != nil {
				return err
			}
			f.nodeCount, _ = strconv.Atoi(count)
		} else {
			var clusters []string
			if err := step(ctx, "Listing EKS clusters", func(sctx context.Context) error {
				var lerr error
				clusters, lerr = b.ListClusters(sctx)
				return lerr
			}); err != nil {
				return err
			}
			if len(clusters) == 0 {
				return fmt.Errorf("no EKS clusters in %s; choose create instead", f.region)
			}
			if err := askOne(huh.NewSelect[string]().Title("Cluster").Options(huh.NewOptions(clusters...)...).Value(&f.existingCluster)); err != nil {
				return err
			}
		}
	}

	clusterName := f.existingCluster
	var nodeRoles []string
	if create {
		clusterName = f.clusterName
		if !clusterNameRe.MatchString(clusterName) {
			return fmt.Errorf("invalid cluster name %q", clusterName)
		}
		if interactive() {
			confirmed := false
			if err := askOne(huh.NewConfirm().
				Title(fmt.Sprintf("Provision EKS cluster %q?", clusterName)).
				Description(fmt.Sprintf("account %s (profile %s), region %s\n%d x %s nodes, IAM roles, gVisor, ECR repository, S3 snapshot bucket", b.AccountID, profileName, b.Region, f.nodeCount, f.instanceType)).
				Affirmative("Provision").Negative("Cancel").
				Value(&confirmed)); err != nil {
				return err
			}
			if !confirmed {
				return errors.New("aborted before provisioning")
			}
		}
		subnets := f.subnetIDs
		if len(subnets) == 0 {
			if err := step(ctx, "Finding default VPC subnets", func(sctx context.Context) error {
				var serr error
				subnets, serr = b.DefaultSubnets(sctx)
				return serr
			}); err != nil {
				return err
			}
		}
		var clusterRole, nodeRole string
		if err := step(ctx, "Ensuring IAM roles", func(sctx context.Context) error {
			var rerr error
			if clusterRole, rerr = b.EnsureClusterRole(sctx); rerr != nil {
				return rerr
			}
			nodeRole, rerr = b.EnsureNodeRole(sctx)
			return rerr
		}); err != nil {
			return err
		}
		nodeRoles = []string{nodeRole}
		if err := step(ctx, fmt.Sprintf("Creating EKS cluster %s (~12 min)", clusterName), func(sctx context.Context) error {
			if cerr := b.CreateCluster(sctx, clusterName, clusterRole, subnets); cerr != nil {
				return cerr
			}
			return b.WaitClusterActive(sctx, clusterName)
		}); err != nil {
			return err
		}
		logger.Info("cluster active", "name", clusterName)
		if err := step(ctx, fmt.Sprintf("Creating nodegroup (%d x %s)", f.nodeCount, f.instanceType), func(sctx context.Context) error {
			if nerr := b.CreateNodegroup(sctx, clusterName, nodeRole, f.instanceType, int32(f.nodeCount), subnets); nerr != nil {
				return nerr
			}
			return b.WaitNodegroupActive(sctx, clusterName)
		}); err != nil {
			return err
		}
		logger.Info("nodegroup active", "instanceType", f.instanceType, "count", f.nodeCount)
	}

	cluster, err := b.DescribeCluster(ctx, clusterName)
	if err != nil {
		return err
	}
	if cluster.Status != "ACTIVE" {
		return fmt.Errorf("cluster %s is %s, not ACTIVE", clusterName, cluster.Status)
	}
	if !create {
		if nodeRoles, err = b.NodegroupRoles(ctx, clusterName); err != nil {
			return err
		}
		if len(nodeRoles) == 0 {
			return fmt.Errorf("cluster %s has no managed nodegroups; self-managed nodes are not supported by bootstrap-aws", clusterName)
		}
	}

	clientset, err := b.Clientset(ctx, cluster)
	if err != nil {
		return err
	}
	if _, err := clientset.Discovery().ServerVersion(); err != nil {
		if !apierrors.IsUnauthorized(err) && !apierrors.IsForbidden(err) {
			return fmt.Errorf("reach cluster %s: %w", clusterName, err)
		}
		mode := ""
		if cluster.AccessConfig != nil {
			mode = string(cluster.AccessConfig.AuthenticationMode)
		}
		if !strings.Contains(mode, "API") {
			return fmt.Errorf("no access to cluster %s and its authentication mode is %s: add your principal to the aws-auth ConfigMap or enable access entries", clusterName, mode)
		}
		if err := b.EnsureAccess(ctx, clusterName); err != nil {
			return err
		}
		if clientset, err = b.Clientset(ctx, cluster); err != nil {
			return err
		}
		if _, err := clientset.Discovery().ServerVersion(); err != nil {
			return fmt.Errorf("cluster access still failing after granting an access entry: %w", err)
		}
	}

	kubeContext, err := b.WriteKubeconfig(cluster)
	if err != nil {
		return err
	}
	if !have("aws") {
		logger.Warn("aws CLI not found: the kubeconfig entry uses `aws eks get-token`, install the AWS CLI for kubectl access")
	}
	logger.Info("kubeconfig updated", "context", kubeContext)

	if err := step(ctx, "Installing gVisor on all nodes", func(sctx context.Context) error {
		if gerr := awsboot.EnsureGVisor(sctx, clientset); gerr != nil {
			return gerr
		}
		return awsboot.WaitGVisorReady(sctx, clientset, 5*time.Minute)
	}); err != nil {
		return err
	}
	logger.Info("gvisor installed", "runtimeClass", "gvisor")

	platform := f.imagePlatform
	if platform == "" {
		platform = "linux/amd64"
		if create && awsboot.IsGraviton(f.instanceType) {
			platform = "linux/arm64"
		}
	}
	var repoURI string
	if err := step(ctx, "Ensuring ECR repository", func(sctx context.Context) error {
		var rerr error
		repoURI, rerr = b.EnsureRepo(sctx, "brickie")
		return rerr
	}); err != nil {
		return err
	}
	imageRef := repoURI + ":" + f.imageTag
	registry, user, password, err := b.RegistryLogin(ctx)
	if err != nil {
		return err
	}
	if err := runStdin(password, "docker", "login", "--username", user, "--password-stdin", registry); err != nil {
		return err
	}
	say("Building and pushing %s (%s)", imageRef, platform)
	if err := run("docker", "build", "--platform", platform, "-t", imageRef, "."); err != nil {
		return err
	}
	if err := run("docker", "push", imageRef); err != nil {
		return err
	}

	bucket := b.SnapshotBucketName()
	if err := step(ctx, "Ensuring S3 snapshot bucket "+bucket, func(sctx context.Context) error {
		if berr := b.EnsureBucket(sctx, bucket); berr != nil {
			return berr
		}
		for _, role := range nodeRoles {
			if perr := b.AttachSnapshotPolicy(sctx, role, bucket); perr != nil {
				return perr
			}
		}
		return nil
	}); err != nil {
		return err
	}

	restCfg, err := b.RESTConfig(ctx, cluster)
	if err != nil {
		return err
	}
	agentSandboxEnabled := true
	if _, err := clientset.Discovery().ServerResourcesForGroupVersion("agents.x-k8s.io/v1beta1"); err == nil {
		if installed, herr := awsboot.ReleaseExists(restCfg, f.namespace, f.release); herr == nil && !installed {
			logger.Info("agent-sandbox already installed outside helm; skipping subchart")
			agentSandboxEnabled = false
		}
	}
	objectStore := fmt.Sprintf("s3://%s?region=%s", bucket, b.Region)
	values := map[string]any{
		"hatchet":       map[string]any{"token": f.token},
		"image":         map[string]any{"repository": repoURI, "tag": f.imageTag},
		"agent-sandbox": map[string]any{"enabled": agentSandboxEnabled},
		"csi":           map[string]any{"enabled": true, "objectStore": objectStore},
		"objectStore":   objectStore,
	}
	sandboxValues := map[string]any{}
	if f.sandboxImg != "" {
		sandboxValues["image"] = f.sandboxImg
	}
	credentials := map[string]any{}
	if f.anthropic != "" {
		credentials["ANTHROPIC_API_KEY"] = f.anthropic
	}
	if f.claudeOAuth != "" {
		credentials["CLAUDE_CODE_OAUTH_TOKEN"] = f.claudeOAuth
	}
	if len(credentials) > 0 {
		sandboxValues["credentials"] = credentials
	}
	if len(sandboxValues) > 0 {
		values["sandbox"] = sandboxValues
	}
	if err := step(ctx, "Installing the brickie chart", func(sctx context.Context) error {
		return awsboot.InstallChart(sctx, restCfg, f.namespace, f.release, f.chart, values)
	}); err != nil {
		return err
	}

	if err := step(ctx, "Waiting for the provider worker", func(sctx context.Context) error {
		return waitDeploymentReady(sctx, clientset, f.namespace, []string{f.release, f.release + "-brickie"}, 5*time.Minute)
	}); err != nil {
		return err
	}

	summary := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Render(strings.Join([]string{
		"brickie is running on EKS",
		"cluster:    " + clusterName + " (" + b.Region + ")",
		"context:    " + kubeContext,
		"image:      " + imageRef,
		"snapshots:  " + objectStore,
		"sandboxes:  gVisor (runtime class \"gvisor\")",
	}, "\n"))
	fmt.Fprintln(os.Stderr, "\n"+summary)
	return nil
}

func waitDeploymentReady(ctx context.Context, clientset *kubernetes.Clientset, namespace string, names []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, name := range names {
			d, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				continue
			}
			if d.Status.ReadyReplicas > 0 && d.Status.ReadyReplicas == d.Status.Replicas {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("deployment %s not ready within %s", strings.Join(names, "|"), timeout)
}
