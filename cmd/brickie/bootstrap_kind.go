package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func bootstrapKindCmd() *cobra.Command {
	var (
		namespace   string
		cluster     string
		release     string
		chart       string
		image       string
		token       string
		anthropic   string
		claudeOAuth string
		sandboxImg  string
	)
	cmd := &cobra.Command{
		Use:   "bootstrap-kind",
		Short: "Create a kind cluster and deploy the sandbox provider with agent-sandbox via Helm",
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "" {
				token = os.Getenv("HATCHET_CLIENT_TOKEN")
			}
			if anthropic == "" {
				anthropic = os.Getenv("ANTHROPIC_API_KEY")
			}
			if claudeOAuth == "" {
				claudeOAuth = os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
			}
			if token == "" {
				return errors.New("HATCHET_CLIENT_TOKEN is required: export it or pass --hatchet-token")
			}
			say("Checking tools")
			for _, bin := range []string{"docker", "helm", "kubectl"} {
				if !have(bin) {
					return fmt.Errorf("%s is required", bin)
				}
			}
			if _, err := output("docker", "info"); err != nil {
				return errors.New("start Docker first")
			}
			if !have("kind") {
				if !have("brew") {
					return errors.New("kind is required: https://kind.sigs.k8s.io")
				}
				if err := run("brew", "install", "kind"); err != nil {
					return err
				}
			}

			say("Kind cluster %q", cluster)
			clusters, _ := output("kind", "get", "clusters")
			exists := false
			for c := range strings.SplitSeq(strings.TrimSpace(clusters), "\n") {
				if c == cluster {
					exists = true
				}
			}
			if !exists {
				if err := run("kind", "create", "cluster", "--name", cluster); err != nil {
					return err
				}
			}
			if err := run("kubectl", "config", "use-context", "kind-"+cluster); err != nil {
				return err
			}

			say("Building provider image %s", image)
			if err := run("docker", "build", "-t", image, "."); err != nil {
				return err
			}
			if err := run("kind", "load", "docker-image", image, "--name", cluster); err != nil {
				return err
			}

			say("Installing chart into namespace %q", namespace)
			agentSandboxEnabled := "true"
			_, crdErr := output("kubectl", "get", "crd", "sandboxes.agents.x-k8s.io")
			_, releaseErr := output("helm", "status", release, "-n", namespace)
			if crdErr == nil && releaseErr != nil {
				fmt.Println("agent-sandbox already installed outside helm; skipping subchart")
				agentSandboxEnabled = "false"
			}
			repo, tag := image, "latest"
			if i := strings.LastIndex(image, ":"); i > 0 {
				repo, tag = image[:i], image[i+1:]
			}
			helmArgs := []string{
				"upgrade", "--install", release, chart,
				"--namespace", namespace, "--create-namespace",
				"--set", "hatchet.token=" + token,
				"--set", "image.repository=" + repo,
				"--set", "image.tag=" + tag,
				"--set", "agent-sandbox.enabled=" + agentSandboxEnabled,
			}
			if sandboxImg != "" {
				helmArgs = append(helmArgs, "--set", "sandbox.image="+sandboxImg)
			}
			if anthropic != "" {
				helmArgs = append(helmArgs, "--set", "sandbox.credentials.ANTHROPIC_API_KEY="+anthropic)
			}
			if claudeOAuth != "" {
				helmArgs = append(helmArgs, "--set", "sandbox.credentials.CLAUDE_CODE_OAUTH_TOKEN="+claudeOAuth)
			}
			if err := run("helm", helmArgs...); err != nil {
				return err
			}

			say("Waiting for the provider worker")
			if err := run("kubectl", "-n", namespace, "rollout", "status", "deploy/"+release+"-brickie", "--timeout=180s"); err != nil {
				return err
			}
			say("Done. Sandbox turns will run in namespace %q on kind-%s.", namespace, cluster)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", envOr("NAMESPACE", "brickie"), "namespace for the provider and sandboxes")
	cmd.Flags().StringVar(&cluster, "cluster", "brickie", "kind cluster name")
	cmd.Flags().StringVar(&release, "release", "brickie", "helm release name")
	cmd.Flags().StringVar(&chart, "chart", "helm/brickie", "path to the brickie chart")
	cmd.Flags().StringVar(&image, "image", "brickie-worker:latest", "provider image to build and load")
	cmd.Flags().StringVar(&sandboxImg, "sandbox-image", "", "default sandbox image (chart default: alpine:3)")
	cmd.Flags().StringVar(&token, "hatchet-token", "", "Hatchet client token (default $HATCHET_CLIENT_TOKEN)")
	cmd.Flags().StringVar(&anthropic, "anthropic-api-key", "", "inject ANTHROPIC_API_KEY into sandboxes (default $ANTHROPIC_API_KEY)")
	cmd.Flags().StringVar(&claudeOAuth, "claude-oauth-token", "", "inject CLAUDE_CODE_OAUTH_TOKEN into sandboxes (default $CLAUDE_CODE_OAUTH_TOKEN)")
	return cmd
}
