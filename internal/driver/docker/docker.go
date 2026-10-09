// Package docker runs sandboxes as containers on a Docker engine, with each
// workspace a directory on this host bind-mounted into its container.
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/hatchet-dev/brickie/internal/driver/fsops"
	"github.com/hatchet-dev/brickie/internal/engine"
)

const (
	workspacePath = "/workspace"
	label         = "brickie.hatchet.run/sandbox"
)

type execResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type liveSandbox struct {
	mu       sync.Mutex
	dir      string
	ttl      time.Duration
	lastUsed time.Time
	daemon   *sandboxdClient
}

// Options configures the driver; zero values take the defaults.
type Options struct {
	IdleTTL      time.Duration
	MaxFileBytes int64
	Staging      fsops.Staging
}

// Driver owns sandbox lifecycle, snapshots, and turn dispatch on one Docker engine.
type Driver struct {
	cli      *client.Client
	opts     Options
	sandboxd string

	mu   sync.Mutex
	live map[string]*liveSandbox
}

// New connects to the engine from the environment, removes containers left by
// a previous provider, and starts the idle reaper.
func New(opts Options) (*Driver, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("docker: %w", err)
	}
	if _, err := cli.Ping(context.Background(), client.PingOptions{}); err != nil {
		return nil, fmt.Errorf("docker engine is not reachable (is Docker running?): %w", err)
	}
	if opts.IdleTTL <= 0 {
		opts.IdleTTL = 15 * time.Minute
	}
	d := &Driver{cli: cli, opts: opts, live: map[string]*liveSandbox{}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if d.sandboxd, err = d.ensureSandboxd(ctx); err != nil {
		return nil, err
	}
	d.prune(ctx)
	go d.reap()
	return d, nil
}

func (d *Driver) prune(ctx context.Context) {
	list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", label),
	})
	if err != nil {
		return
	}
	for _, c := range list.Items {
		_, _ = d.cli.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true})
	}
}

func (d *Driver) ensureImage(ctx context.Context, image string) error {
	if _, err := d.cli.ImageInspect(ctx, image); err == nil || !cerrdefs.IsNotFound(err) {
		return err
	}
	pull, err := d.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	if err := pull.Wait(ctx); err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	return nil
}

func (d *Driver) createContainer(ctx context.Context, name, image, workspace string) (*sandboxdClient, error) {
	if err := d.ensureImage(ctx, image); err != nil {
		return nil, err
	}
	d.removeContainer(ctx, name)
	useInit := true
	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:        image,
			Entrypoint:   []string{sandboxdPath, "--root-dir", workspacePath, "--listen-host", "0.0.0.0"},
			Cmd:          []string{},
			WorkingDir:   workspacePath,
			Labels:       map[string]string{label: "true"},
			ExposedPorts: network.PortSet{network.MustParsePort(sandboxdGRPCPort): struct{}{}},
		},
		HostConfig: &container.HostConfig{
			Mounts: []mount.Mount{
				{Type: mount.TypeBind, Source: workspace, Target: workspacePath},
				{Type: mount.TypeBind, Source: d.sandboxd, Target: sandboxdPath, ReadOnly: true},
			},
			PortBindings: network.PortMap{network.MustParsePort(sandboxdGRPCPort): {{HostIP: netip.MustParseAddr("127.0.0.1")}}},
			Init:         &useInit,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create container %s: %w", name, err)
	}
	if _, err := d.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return nil, fmt.Errorf("start container %s: %w", name, err)
	}
	daemon, err := d.connectSandboxd(ctx, created.ID)
	if err != nil {
		d.removeContainer(ctx, name)
		return nil, fmt.Errorf("sandbox %s: %w", name, err)
	}
	return daemon, nil
}

func (d *Driver) removeContainer(ctx context.Context, name string) {
	_, _ = d.cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true})
}

func (d *Driver) acquire(ctx context.Context, name, image string, ttl time.Duration) (*liveSandbox, bool, error) {
	if ttl <= 0 {
		ttl = d.opts.IdleTTL
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
	daemon, err := d.createContainer(ctx, name, image, workspace)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, false, err
	}
	sb := &liveSandbox{dir: dir, ttl: ttl, lastUsed: time.Now(), daemon: daemon}
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
	d.removeContainer(context.Background(), name)
	if ok {
		sb.daemon.close()
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
		req.Logf("sandbox %s destroyed", req.SandboxName)
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
	execIn := func(ctx context.Context, argv []string, timeout time.Duration) (execResult, error) {
		return sb.daemon.run(ctx, argv, timeout)
	}
	if created {
		req.Logf("sandbox %s created", req.SandboxName)
	} else {
		req.Logf("sandbox %s reused", req.SandboxName)
	}

	// The snapshot tar is agent-influenced, so it is extracted inside the
	// sandbox: a hostile entry can only unpack within the container. The tar is
	// handed over through the workspace mount rather than stdin.
	if created && req.SnapshotTar != nil {
		restoreStart := time.Now()
		const tarName = ".brickie-restore.tar.gz"
		if err := os.WriteFile(filepath.Join(workspace, tarName), req.SnapshotTar, 0o600); err != nil {
			return engine.TurnResponse{}, err
		}
		res, err := execIn(ctx, []string{"sh", "-c", "tar -xzf " + tarName + " -C . && rm -f " + tarName}, 0)
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
		output, mutated, err = fsops.Turn(ctx, workspace, req.Fn, req.Input, fsops.Options{MaxBytes: d.opts.MaxFileBytes, Staging: d.opts.Staging, SandboxID: strings.TrimPrefix(req.SandboxName, "brickie-")})
		if err != nil {
			return engine.TurnResponse{}, err
		}
	case req.Fn == engine.CommandFn:
		var in engine.CommandInput
		if err := json.Unmarshal(req.Input, &in); err != nil {
			return engine.TurnResponse{}, err
		}
		res, err := execIn(ctx, []string{"sh", "-c", in.Cmd}, time.Duration(in.TimeoutMs)*time.Millisecond)
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
