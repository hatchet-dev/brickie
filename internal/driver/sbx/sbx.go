// Package sbx hosts sandboxes in Docker Sandboxes micro-VMs through the sbx CLI.
package sbx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/hatchet-dev/brickie/internal/driver/fsops"
)

var (
	notAuthenticated = regexp.MustCompile(`(?i)not authenticated|sbx login`)
	kitFailure       = regexp.MustCompile(`(?i)failed to apply kit`)
)

// Backend runs sandboxes as Docker Sandboxes micro-VMs.
type Backend struct {
	Agent string
}

// New returns a backend that creates sandboxes from the claude agent kit.
func New() *Backend {
	return &Backend{Agent: "claude"}
}

// Name is the driver name shown in logs.
func (b *Backend) Name() string { return "sbx" }

func run(ctx context.Context, args ...string) (fsops.Result, error) {
	cmd := exec.CommandContext(ctx, "sbx", args...) // #nosec G204 -- args are provider-built
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := fsops.Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	if err != nil {
		return res, err
	}
	return res, nil
}

func mustRun(ctx context.Context, args ...string) error {
	res, err := run(ctx, args...)
	if err != nil {
		return fmt.Errorf("sbx %s: %w", args[0], err)
	}
	if res.ExitCode != 0 {
		detail := res.Stderr + res.Stdout
		switch {
		case notAuthenticated.MatchString(detail):
			return errors.New("Docker Sandboxes is not authenticated: run `sbx login` (or `hack/run-demo.sh --driver sbx`) and retry")
		case kitFailure.MatchString(detail):
			return errors.New("Docker Sandboxes could not apply the agent kit to this image: `template` must be an sbx template built from the Docker Sandboxes base image, not an arbitrary Docker image")
		}
		return fmt.Errorf("sbx %s failed: %s", args[0], strings.TrimSpace(detail))
	}
	return nil
}

// Prune removes sandboxes left by a previous provider process.
func (b *Backend) Prune(ctx context.Context) {
	res, err := run(ctx, "ls")
	if err != nil || res.ExitCode != 0 {
		return
	}
	for line := range strings.SplitSeq(res.Stdout, "\n") {
		name := strings.Fields(line)
		if len(name) > 0 && strings.HasPrefix(name[0], "brickie-") {
			_, _ = run(ctx, "rm", "-f", name[0])
		}
	}
}

// Create makes a sandbox with the workspace mounted, replacing a stale one of the same name.
func (b *Backend) Create(ctx context.Context, name, image, workspace string) error {
	args := []string{"create", b.Agent, "--name", name, "-q"}
	if image != "" {
		args = append(args, "-t", image)
	}
	args = append(args, workspace)
	err := mustRun(ctx, args...)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		return err
	}
	if _, err := run(ctx, "rm", "-f", name); err != nil {
		return err
	}
	return mustRun(ctx, args...)
}

// Exec runs argv in the sandbox with the workspace as the working directory.
func (b *Backend) Exec(ctx context.Context, name, workspace string, argv []string) (fsops.Result, error) {
	return run(ctx, append([]string{"exec", "-w", workspace, name}, argv...)...)
}

// Destroy removes the sandbox.
func (b *Backend) Destroy(ctx context.Context, name string) {
	_, _ = run(ctx, "rm", "-f", name)
}
