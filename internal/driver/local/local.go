// Package local runs sandboxes whose workspace is a directory on this host.
package local

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hatchet-dev/brickie/internal/driver/fsops"
	"github.com/hatchet-dev/brickie/internal/engine"
)

// Backend is a container engine the driver creates, execs into, and removes sandboxes with.
type Backend interface {
	Name() string
	Create(ctx context.Context, name, image, workspace string) error
	Exec(ctx context.Context, name, workspace string, argv []string) (fsops.Result, error)
	Destroy(ctx context.Context, name string)
	Prune(ctx context.Context)
}

type liveSandbox struct {
	mu       sync.Mutex
	dir      string
	ttl      time.Duration
	lastUsed time.Time
}

// Driver owns sandbox lifecycle, snapshots, and turn dispatch for a Backend.
type Driver struct {
	backend Backend
	idleTTL time.Duration

	mu   sync.Mutex
	live map[string]*liveSandbox
}

// New returns a driver whose sandboxes are destroyed after idleTTL without a turn.
func New(backend Backend, idleTTL time.Duration) *Driver {
	if idleTTL <= 0 {
		idleTTL = 15 * time.Minute
	}
	d := &Driver{backend: backend, idleTTL: idleTTL, live: map[string]*liveSandbox{}}
	backend.Prune(context.Background())
	go d.reap()
	return d
}

func (d *Driver) acquire(ctx context.Context, name, image string, ttl time.Duration) (*liveSandbox, bool, error) {
	if ttl <= 0 {
		ttl = d.idleTTL
	}
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
	if err := d.backend.Create(ctx, name, image, workspace); err != nil {
		_ = os.RemoveAll(dir)
		return nil, false, err
	}
	sb := &liveSandbox{dir: dir, ttl: ttl, lastUsed: time.Now()}
	d.live[name] = sb
	return sb, true, nil
}

func (d *Driver) lookup(name string) *liveSandbox {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.live[name]
}

func (d *Driver) destroy(name string) {
	d.mu.Lock()
	sb, ok := d.live[name]
	delete(d.live, name)
	d.mu.Unlock()
	d.backend.Destroy(context.Background(), name)
	if ok {
		_ = os.RemoveAll(sb.dir)
	}
}

func (d *Driver) reap() {
	for range time.Tick(time.Minute) {
		d.mu.Lock()
		var idle []string
		for name, sb := range d.live {
			if time.Since(sb.lastUsed) > sb.ttl {
				idle = append(idle, name)
			}
		}
		d.mu.Unlock()
		for _, name := range idle {
			d.destroy(name)
		}
	}
}

// Live reports the running sandboxes and when each last ran a turn.
func (d *Driver) Live() map[string]time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]time.Time, len(d.live))
	for name, sb := range d.live {
		out[name] = sb.lastUsed
	}
	return out
}

// Close destroys every live sandbox.
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

// RunTurn executes one turn in the named sandbox; create and connect start it, other turns need it running.
func (d *Driver) RunTurn(ctx context.Context, req engine.TurnRequest) (engine.TurnResponse, error) {
	if req.Fn == engine.DestroyFn {
		d.destroy(req.SandboxName)
		req.Logf("%s sandbox %s destroyed", d.backend.Name(), req.SandboxName)
		return engine.TurnResponse{Output: json.RawMessage(`{"destroyed":true}`)}, nil
	}

	timings := map[string]int64{}
	phase := func(name string, start time.Time) {
		timings[name] = time.Since(start).Milliseconds()
	}

	claimStart := time.Now()
	var sb *liveSandbox
	var created bool
	var err error
	if req.Fn == engine.CreateFn || req.Fn == engine.ConnectFn {
		sb, created, err = d.acquire(ctx, req.SandboxName, req.Image, req.IdleTTL)
		if err != nil {
			return engine.TurnResponse{}, err
		}
	} else if sb = d.lookup(req.SandboxName); sb == nil {
		return engine.TurnResponse{}, fmt.Errorf("sandbox %s is not running on this provider (idle-expired, killed, or started on another provider): connect to it first", strings.TrimPrefix(req.SandboxName, "brickie-"))
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.lastUsed = time.Now()
	phase("claim", claimStart)
	workspace := filepath.Join(sb.dir, "workspace")
	execIn := func(ctx context.Context, argv []string) (fsops.Result, error) {
		return d.backend.Exec(ctx, req.SandboxName, workspace, argv)
	}
	if created {
		req.Logf("%s sandbox %s created", d.backend.Name(), req.SandboxName)
	} else {
		req.Logf("%s sandbox %s reused", d.backend.Name(), req.SandboxName)
	}

	// The snapshot tar is agent-influenced, so it is extracted inside the
	// sandbox: a hostile entry can only unpack within the sandbox. The tar is
	// handed over through the workspace mount rather than stdin.
	if created && req.SnapshotTar != nil {
		restoreStart := time.Now()
		const tarName = ".brickie-restore.tar.gz"
		if err := os.WriteFile(filepath.Join(workspace, tarName), req.SnapshotTar, 0o600); err != nil {
			return engine.TurnResponse{}, err
		}
		res, err := execIn(ctx, []string{"sh", "-c", "tar -xzf " + tarName + " -C . && rm -f " + tarName})
		if err != nil {
			return engine.TurnResponse{}, fmt.Errorf("restore snapshot: %w", err)
		}
		if res.ExitCode != 0 {
			return engine.TurnResponse{}, fmt.Errorf("restore snapshot (exit %d): %s", res.ExitCode, strings.TrimSpace(res.Stderr))
		}
		phase("restore", restoreStart)
		req.Logf("workspace restored from snapshot tar")
	}

	if req.Fn == engine.CreateFn || req.Fn == engine.ConnectFn {
		out, _ := json.Marshal(map[string]bool{"created": created})
		return engine.TurnResponse{Output: out, Timings: timings}, nil
	}

	execStart := time.Now()
	var output any
	mutated := true
	switch {
	case strings.HasPrefix(req.Fn, fsops.Prefix):
		output, mutated, err = fsops.Turn(ctx, workspace, req.Fn, req.Input, execIn)
		if err != nil {
			return engine.TurnResponse{}, err
		}
	case req.Fn == engine.CommandFn:
		var in engine.CommandInput
		if err := json.Unmarshal(req.Input, &in); err != nil {
			return engine.TurnResponse{}, err
		}
		res, err := execIn(ctx, []string{"sh", "-c", in.Cmd})
		if err != nil {
			return engine.TurnResponse{}, err
		}
		output = engine.CommandOutput{Stdout: strings.TrimSpace(res.Stdout), Stderr: strings.TrimSpace(res.Stderr), ExitCode: res.ExitCode}
	default:
		return engine.TurnResponse{}, fmt.Errorf("unknown sandbox fn %q", req.Fn)
	}

	phase("exec", execStart)
	req.Logf("fn %s executed in %s", req.Fn, time.Since(execStart).Round(time.Millisecond))
	outJSON, err := json.Marshal(output)
	if err != nil {
		return engine.TurnResponse{}, err
	}
	if !mutated {
		return engine.TurnResponse{Output: outJSON, Timings: timings}, nil
	}
	checkpointStart := time.Now()
	outTar := filepath.Join(sb.dir, "out.tar.gz")
	if out, err := exec.CommandContext(ctx, "tar", "-czf", outTar, "-C", workspace, ".").CombinedOutput(); err != nil { // #nosec G204 -- paths are provider-built
		return engine.TurnResponse{}, fmt.Errorf("snapshot workspace: %s", out)
	}
	snapshot, err := os.ReadFile(outTar) // #nosec G304 -- outTar is the provider's own temp file
	if err != nil {
		return engine.TurnResponse{}, err
	}
	phase("checkpoint", checkpointStart)
	return engine.TurnResponse{Output: outJSON, SnapshotTar: snapshot, Timings: timings}, nil
}
