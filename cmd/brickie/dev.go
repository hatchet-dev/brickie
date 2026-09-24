package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/lipgloss"
	charmlog "github.com/charmbracelet/log"
	"github.com/spf13/cobra"
)

type devState struct {
	ClaudeAuthed bool `json:"claudeAuthed"`
}

func devStatePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "brickie", "dev.json")
	}
	return filepath.Join(home, ".brickie", "dev.json")
}

func readDevState() devState {
	var s devState
	if b, err := os.ReadFile(devStatePath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func writeDevState(s devState) {
	p := devStatePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	b, _ := json.Marshal(s)
	_ = os.WriteFile(p, b, 0o644)
}

func devCmd() *cobra.Command {
	claudeAuth := false
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Set up this machine to run brickie sandboxes locally with Docker Sandboxes",
		Long: "Checks and configures everything a local sandbox turn needs: Docker Desktop,\n" +
			"the Docker Sandboxes CLI (sbx), the sandbox network policy, and Claude\n" +
			"authentication inside sandboxes. Safe to re-run any time; every step is\n" +
			"skipped once it holds. Finishes with exactly what to run next.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDev(cmd.Context(), claudeAuth)
		},
	}
	cmd.Flags().BoolVar(&claudeAuth, "claude-auth", false, "re-run Claude authentication inside sandboxes")
	return cmd
}

func runDev(ctx context.Context, claudeAuth bool) error {
	logger := charmlog.New(os.Stderr)

	if err := step(ctx, "Checking Docker Desktop", func(context.Context) error {
		if !have("docker") {
			return errors.New("install Docker Desktop first: https://www.docker.com/products/docker-desktop/")
		}
		if _, err := output("docker", "info"); err != nil {
			return errors.New("start Docker Desktop, then re-run brickie dev")
		}
		return nil
	}); err != nil {
		return err
	}
	logger.Info("docker ready")

	if !have("sbx") {
		if runtime.GOOS != "darwin" {
			return errors.New("Docker Sandboxes (sbx) is required and currently ships with Docker Desktop on macOS: https://docs.docker.com/ai/sandboxes/")
		}
		if !have("brew") {
			return errors.New("Homebrew is required to install sbx: https://brew.sh")
		}
		say("Installing sbx (Docker Sandboxes CLI)")
		if err := run("brew", "trust", "docker/tap"); err != nil {
			return err
		}
		if err := run("brew", "install", "docker/tap/sbx"); err != nil {
			return err
		}
	}
	logger.Info("sbx ready")

	if _, err := output("sbx", "ls"); err != nil {
		say("Signing in to Docker Sandboxes (opens a browser)")
		if err := run("sbx", "login"); err != nil {
			return err
		}
	}
	logger.Info("docker sandboxes signed in")

	if err := step(ctx, "Checking sandbox network policy", func(context.Context) error {
		out, _ := output("sbx", "policy", "ls")
		if strings.Contains(out, "balanced") || strings.Contains(out, "local-policy") {
			return nil
		}
		return run("sbx", "policy", "init", "balanced")
	}); err != nil {
		return err
	}
	logger.Info("network policy in place")

	state := readDevState()
	if claudeAuth || !state.ClaudeAuthed {
		say("Authenticating Claude inside sandboxes (opens a browser)")
		if err := run("sbx", "run", "claude", "--", "auth", "login"); err != nil {
			return err
		}
		state.ClaudeAuthed = true
		writeDevState(state)
	}
	logger.Info("claude authenticated in sandboxes")

	for _, bin := range []string{"node", "pnpm"} {
		if !have(bin) {
			logger.Warn("missing tool for the TypeScript examples", "tool", bin, "install", "https://pnpm.io/installation")
		}
	}

	tokenSet := os.Getenv("HATCHET_CLIENT_TOKEN") != ""
	if tokenSet && os.Getenv("HATCHET_CLIENT_TLS_ROOT_CA_FILE") != "" {
		logger.Warn("HATCHET_CLIENT_TLS_ROOT_CA_FILE is set; unset it before targeting a hosted Hatchet instance")
	}

	fmt.Fprintln(os.Stderr, devSummary(tokenSet))
	return nil
}

func devSummary(tokenSet bool) string {
	hatchetLine := "  hatchet      no HATCHET_CLIENT_TOKEN set — an embedded engine runs in ~/.hatchet;"
	hatchetDetail := "               nothing leaves this machine except Claude API calls"
	if tokenSet {
		hatchetLine = "  hatchet      HATCHET_CLIENT_TOKEN is set — turns orchestrate through that instance;"
		hatchetDetail = "               sandboxes still run only on this machine"
	}
	lines := []string{
		"brickie is ready",
		"",
		"  sandboxes    Docker Sandboxes micro-VMs on this machine",
		"               no host env vars are injected; the per-turn workspace dir is the only shared path",
		"  snapshots    ~/.brickie/snapshots (pruned after 7 days)",
		hatchetLine,
		hatchetDetail,
		"",
		"  try it:      pnpm install   (repo root, once)",
		"               cd examples/typescript && pnpm demo",
	}
	return "\n" + lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Render(strings.Join(lines, "\n"))
}
