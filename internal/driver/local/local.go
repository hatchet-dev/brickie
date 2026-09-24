package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hatchet-dev/brickie/internal/engine"
)

var (
	notAuthenticated = regexp.MustCompile(`(?i)not authenticated|sbx login`)
	kitFailure       = regexp.MustCompile(`(?i)failed to apply kit`)
)

type Driver struct {
	Agent string
}

func New() *Driver {
	return &Driver{Agent: "claude"}
}

type execResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func runSbx(ctx context.Context, args ...string) (execResult, error) {
	cmd := exec.CommandContext(ctx, "sbx", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := execResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	if err != nil {
		return res, err
	}
	return res, nil
}

func sbx(ctx context.Context, args ...string) (string, error) {
	res, err := runSbx(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("sbx %s: %w", args[0], err)
	}
	if res.ExitCode != 0 {
		detail := res.Stderr + res.Stdout
		switch {
		case notAuthenticated.MatchString(detail):
			return "", errors.New("Docker Sandboxes is not authenticated: run `sbx login` (or `hack/run-demo.sh`) and retry")
		case kitFailure.MatchString(detail):
			return "", errors.New("Docker Sandboxes could not apply the agent kit to this image: `image` must be an sbx template built from the Docker Sandboxes base image, not an arbitrary Docker image")
		}
		return "", fmt.Errorf("sbx %s failed: %s", args[0], strings.TrimSpace(detail))
	}
	return res.Stdout, nil
}

func listFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{}
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return engine.FilterFiles(names)
}

func (d *Driver) RunTurn(ctx context.Context, req engine.TurnRequest) (engine.TurnResponse, error) {
	dir, err := os.MkdirTemp("", req.SandboxName+"-")
	if err != nil {
		return engine.TurnResponse{}, err
	}
	defer os.RemoveAll(dir)
	workspace := filepath.Join(dir, "workspace")
	ctrl := filepath.Join(workspace, ".brickie")
	if err := os.MkdirAll(ctrl, 0o755); err != nil {
		return engine.TurnResponse{}, err
	}
	if req.SnapshotTar != nil {
		inTar := filepath.Join(dir, "in.tar.gz")
		if err := os.WriteFile(inTar, req.SnapshotTar, 0o644); err != nil {
			return engine.TurnResponse{}, err
		}
		if out, err := exec.CommandContext(ctx, "tar", "-xzf", inTar, "-C", workspace).CombinedOutput(); err != nil {
			return engine.TurnResponse{}, fmt.Errorf("restore snapshot: %s", out)
		}
	}

	timings := map[string]int64{}
	phase := func(name string, start time.Time) {
		timings[name] = time.Since(start).Milliseconds()
	}
	createStart := time.Now()
	createArgs := []string{"create", d.Agent, "--name", req.SandboxName, "-q"}
	if req.Image != "" {
		createArgs = append(createArgs, "-t", req.Image)
	}
	createArgs = append(createArgs, workspace)
	if _, err := sbx(ctx, createArgs...); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			return engine.TurnResponse{}, err
		}
		if _, err := runSbx(ctx, "rm", "-f", req.SandboxName); err != nil {
			return engine.TurnResponse{}, err
		}
		req.Logf("removed leftover sandbox %s from an interrupted attempt", req.SandboxName)
		if _, err := sbx(ctx, createArgs...); err != nil {
			return engine.TurnResponse{}, err
		}
	}
	phase("claim", createStart)
	req.Logf("sbx sandbox %s created", req.SandboxName)
	defer func() {
		_, _ = runSbx(context.Background(), "rm", "-f", req.SandboxName)
		req.Logf("sbx sandbox %s removed", req.SandboxName)
	}()

	execStart := time.Now()
	var output any
	switch req.Fn {
	case engine.CommandFn:
		var in engine.CommandInput
		if err := json.Unmarshal(req.Input, &in); err != nil {
			return engine.TurnResponse{}, err
		}
		res, err := runSbx(ctx, "exec", "-w", workspace, req.SandboxName, "sh", "-c", in.Cmd)
		if err != nil {
			return engine.TurnResponse{}, err
		}
		output = engine.CommandOutput{Stdout: strings.TrimSpace(res.Stdout), Stderr: strings.TrimSpace(res.Stderr), ExitCode: res.ExitCode}
	case engine.PromptFn:
		var in engine.PromptInput
		if err := json.Unmarshal(req.Input, &in); err != nil {
			return engine.TurnResponse{}, err
		}
		args := append([]string{"exec", "-w", workspace, req.SandboxName}, engine.PromptCommand(workspace, in.Prompt)...)
		res, err := runSbx(ctx, args...)
		if err != nil {
			return engine.TurnResponse{}, err
		}
		if res.ExitCode != 0 {
			return engine.TurnResponse{}, fmt.Errorf("claude failed (exit %d): %s", res.ExitCode, strings.TrimSpace(res.Stderr+res.Stdout))
		}
		if apiMs := engine.ParsePromptAPIMs(res.Stdout); apiMs > 0 {
			timings["llm"] = apiMs
		}
		output = engine.PromptOutput{Reply: engine.ParsePromptReply(res.Stdout), Files: listFiles(workspace)}
	default:
		if req.FnSource == "" {
			return engine.TurnResponse{}, fmt.Errorf("unknown sandbox fn %q and no fnSource provided", req.Fn)
		}
		if err := os.WriteFile(filepath.Join(ctrl, "runner.cjs"), []byte(engine.RunnerJS), 0o644); err != nil {
			return engine.TurnResponse{}, err
		}
		if err := os.WriteFile(filepath.Join(ctrl, "input.json"), engine.RunnerInput(req.FnSource, req.Input), 0o644); err != nil {
			return engine.TurnResponse{}, err
		}
		if _, err := sbx(ctx, "exec", "-w", workspace, req.SandboxName, "node", ".brickie/runner.cjs"); err != nil {
			return engine.TurnResponse{}, err
		}
		raw, err := os.ReadFile(filepath.Join(ctrl, "output.json"))
		if err != nil {
			return engine.TurnResponse{}, fmt.Errorf("read turn output: %w", err)
		}
		output = json.RawMessage(raw)
	}

	phase("exec", execStart)
	req.Logf("fn %s executed in %s", req.Fn, time.Since(execStart).Round(time.Millisecond))
	outJSON, err := json.Marshal(output)
	if err != nil {
		return engine.TurnResponse{}, err
	}
	_ = os.RemoveAll(ctrl)
	checkpointStart := time.Now()
	outTar := filepath.Join(dir, "out.tar.gz")
	if out, err := exec.CommandContext(ctx, "tar", "-czf", outTar, "-C", workspace, ".").CombinedOutput(); err != nil {
		return engine.TurnResponse{}, fmt.Errorf("snapshot workspace: %s", out)
	}
	snapshot, err := os.ReadFile(outTar)
	if err != nil {
		return engine.TurnResponse{}, err
	}
	phase("checkpoint", checkpointStart)
	return engine.TurnResponse{Output: outJSON, SnapshotTar: snapshot, Timings: timings}, nil
}
