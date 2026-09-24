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
	"sync"
	"time"

	"github.com/hatchet-dev/brickie/internal/engine"
)

var (
	notAuthenticated = regexp.MustCompile(`(?i)not authenticated|sbx login`)
	kitFailure       = regexp.MustCompile(`(?i)failed to apply kit`)
)

type liveSandbox struct {
	mu       sync.Mutex
	dir      string
	lastUsed time.Time
}

type Driver struct {
	Agent   string
	IdleTTL time.Duration

	mu   sync.Mutex
	live map[string]*liveSandbox
}

// New returns a driver whose sandboxes are destroyed after idleTTL without a turn.
func New(idleTTL time.Duration) *Driver {
	if idleTTL <= 0 {
		idleTTL = 15 * time.Minute
	}
	d := &Driver{Agent: "claude", IdleTTL: idleTTL, live: map[string]*liveSandbox{}}
	d.pruneOrphans()
	go d.reap()
	return d
}

// pruneOrphans removes brickie sandboxes left by a previous provider process;
// their workspace directories died with that process, so they cannot be reused.
func (d *Driver) pruneOrphans() {
	res, err := runSbx(context.Background(), "ls")
	if err != nil || res.ExitCode != 0 {
		return
	}
	for line := range strings.SplitSeq(res.Stdout, "\n") {
		name := strings.Fields(line)
		if len(name) > 0 && strings.HasPrefix(name[0], "brickie-") {
			_, _ = runSbx(context.Background(), "rm", "-f", name[0])
		}
	}
}

type execResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func runSbx(ctx context.Context, args ...string) (execResult, error) {
	return runSbxIn(ctx, nil, args...)
}

func runSbxIn(ctx context.Context, stdin []byte, args ...string) (execResult, error) {
	cmd := exec.CommandContext(ctx, "sbx", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
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

// acquire returns the live sandbox for a name, creating it on first use. A
// sandbox left behind by a previous provider process is replaced, since its
// workspace directory is gone with that process.
func (d *Driver) acquire(ctx context.Context, name, image string) (*liveSandbox, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if sb, ok := d.live[name]; ok {
		return sb, false, nil
	}
	dir, err := os.MkdirTemp("", name+"-")
	if err != nil {
		return nil, false, err
	}
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		return nil, false, err
	}
	createArgs := []string{"create", d.Agent, "--name", name, "-q"}
	if image != "" {
		createArgs = append(createArgs, "-t", image)
	}
	createArgs = append(createArgs, workspace)
	if _, err := sbx(ctx, createArgs...); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			_ = os.RemoveAll(dir)
			return nil, false, err
		}
		if _, err := runSbx(ctx, "rm", "-f", name); err != nil {
			_ = os.RemoveAll(dir)
			return nil, false, err
		}
		if _, err := sbx(ctx, createArgs...); err != nil {
			_ = os.RemoveAll(dir)
			return nil, false, err
		}
	}
	sb := &liveSandbox{dir: dir, lastUsed: time.Now()}
	d.live[name] = sb
	return sb, true, nil
}

func (d *Driver) destroy(name string) {
	d.mu.Lock()
	sb, ok := d.live[name]
	delete(d.live, name)
	d.mu.Unlock()
	_, _ = runSbx(context.Background(), "rm", "-f", name)
	if ok {
		_ = os.RemoveAll(sb.dir)
	}
}

func (d *Driver) reap() {
	for range time.Tick(time.Minute) {
		d.mu.Lock()
		var idle []string
		for name, sb := range d.live {
			if time.Since(sb.lastUsed) > d.IdleTTL {
				idle = append(idle, name)
			}
		}
		d.mu.Unlock()
		for _, name := range idle {
			d.destroy(name)
		}
	}
}

// Close destroys every live sandbox; called when the provider shuts down.
func (d *Driver) Close() {
	d.mu.Lock()
	names := make([]string, 0, len(d.live))
	for name := range d.live {
		names = append(names, name)
	}
	d.mu.Unlock()
	for _, name := range names {
		d.destroy(name)
	}
}

// RunTurn executes one turn in the sandbox named by the request, creating it on first use.
func (d *Driver) RunTurn(ctx context.Context, req engine.TurnRequest) (engine.TurnResponse, error) {
	if req.Fn == engine.DestroyFn {
		d.destroy(req.SandboxName)
		req.Logf("sbx sandbox %s destroyed", req.SandboxName)
		return engine.TurnResponse{Output: json.RawMessage(`{"destroyed":true}`)}, nil
	}

	timings := map[string]int64{}
	phase := func(name string, start time.Time) {
		timings[name] = time.Since(start).Milliseconds()
	}

	claimStart := time.Now()
	sb, created, err := d.acquire(ctx, req.SandboxName, req.Image)
	if err != nil {
		return engine.TurnResponse{}, err
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.lastUsed = time.Now()
	phase("claim", claimStart)
	workspace := filepath.Join(sb.dir, "workspace")
	if created {
		req.Logf("sbx sandbox %s created", req.SandboxName)
	} else {
		req.Logf("sbx sandbox %s reused", req.SandboxName)
	}

	// The snapshot tar is agent-influenced, so it is extracted inside the
	// sandbox: a hostile entry can only unpack within the micro-VM.
	if created && req.SnapshotTar != nil {
		restoreStart := time.Now()
		res, err := runSbxIn(ctx, req.SnapshotTar, "exec", "-w", workspace, req.SandboxName, "tar", "-xzf", "-", "-C", workspace)
		if err != nil {
			return engine.TurnResponse{}, fmt.Errorf("restore snapshot: %w", err)
		}
		if res.ExitCode != 0 {
			return engine.TurnResponse{}, fmt.Errorf("restore snapshot (exit %d): %s", res.ExitCode, strings.TrimSpace(res.Stderr))
		}
		phase("restore", restoreStart)
		req.Logf("workspace restored from snapshot tar")
	}

	if req.Fn == engine.CreateFn {
		return engine.TurnResponse{Output: json.RawMessage(`{"created":true}`), Timings: timings}, nil
	}

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
		history := engine.LoadConversation(workspace)
		args := append([]string{"exec", "-w", workspace, req.SandboxName}, engine.PromptCommand(workspace, in.Prompt, history)...)
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
		reply := engine.ParsePromptReply(res.Stdout)
		if err := engine.AppendConversation(workspace, in.Prompt, reply); err != nil {
			req.Logf("conversation append failed: %v", err)
		}
		output = engine.PromptOutput{Reply: reply, Files: listFiles(workspace)}
	default:
		return engine.TurnResponse{}, fmt.Errorf("unknown sandbox fn %q", req.Fn)
	}

	phase("exec", execStart)
	req.Logf("fn %s executed in %s", req.Fn, time.Since(execStart).Round(time.Millisecond))
	outJSON, err := json.Marshal(output)
	if err != nil {
		return engine.TurnResponse{}, err
	}
	checkpointStart := time.Now()
	outTar := filepath.Join(sb.dir, "out.tar.gz")
	if out, err := exec.CommandContext(ctx, "tar", "-czf", outTar, "-C", workspace, ".").CombinedOutput(); err != nil {
		return engine.TurnResponse{}, fmt.Errorf("snapshot workspace: %s", out)
	}
	snapshot, err := os.ReadFile(outTar) // #nosec G304 -- outTar is the provider's own temp file
	if err != nil {
		return engine.TurnResponse{}, err
	}
	phase("checkpoint", checkpointStart)
	return engine.TurnResponse{Output: outJSON, SnapshotTar: snapshot, Timings: timings}, nil
}
