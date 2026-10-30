// Package docker runs sandboxes as containers on a Docker engine, each with
// sandboxd as its entrypoint.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/hatchet-dev/brickie/internal/driver"
	"github.com/hatchet-dev/brickie/internal/engine"
	"github.com/hatchet-dev/brickie/internal/sandboxd"
)

const (
	label        = "brickie.hatchet.run/sandbox"
	sandboxdPath = "/sandboxd"
)

var (
	grpcPort = network.MustParsePort(strconv.Itoa(sandboxd.GRPCPort) + "/tcp")
	restPort = network.MustParsePort(strconv.Itoa(sandboxd.RESTPort) + "/tcp")
)

type liveSandbox struct {
	mu       sync.Mutex
	ttl      time.Duration
	lastUsed time.Time
	client   *sandboxd.Client
}

// Options configures the driver; zero values take the defaults.
type Options struct {
	IdleTTL      time.Duration
	MaxFileBytes int64
	Staging      driver.Staging
}

type Driver struct {
	cli      *client.Client
	opts     Options
	sandboxd string
	token    string

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
	token, err := sandboxd.NewToken()
	if err != nil {
		return nil, err
	}
	d := &Driver{cli: cli, opts: opts, token: token, live: map[string]*liveSandbox{}}
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

func (d *Driver) createContainer(ctx context.Context, name, image string) (*sandboxd.Client, error) {
	if err := d.ensureImage(ctx, image); err != nil {
		return nil, err
	}
	_ = d.removeContainer(ctx, name)
	useInit := true
	loopback := netip.MustParseAddr("127.0.0.1")
	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:        image,
			Entrypoint:   append([]string{sandboxdPath}, sandboxd.Args(d.token, "0.0.0.0")...),
			Cmd:          []string{},
			WorkingDir:   sandboxd.Workspace,
			Labels:       map[string]string{label: "true"},
			ExposedPorts: network.PortSet{grpcPort: struct{}{}, restPort: struct{}{}},
		},
		HostConfig: &container.HostConfig{
			Mounts:       []mount.Mount{{Type: mount.TypeBind, Source: d.sandboxd, Target: sandboxdPath, ReadOnly: true}},
			PortBindings: network.PortMap{grpcPort: {{HostIP: loopback}}, restPort: {{HostIP: loopback}}},
			Init:         &useInit,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create container %s: %w", name, err)
	}
	if _, err := d.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return nil, fmt.Errorf("start container %s: %w", name, err)
	}
	c, err := d.connect(ctx, created.ID)
	if err != nil {
		_ = d.removeContainer(ctx, name)
		return nil, fmt.Errorf("sandbox %s: %w", name, err)
	}
	return c, nil
}

func (d *Driver) connect(ctx context.Context, containerID string) (*sandboxd.Client, error) {
	inspect, err := d.cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	hostPort := func(p network.Port) string {
		if inspect.Container.NetworkSettings == nil {
			return ""
		}
		for _, b := range inspect.Container.NetworkSettings.Ports[p] {
			return "127.0.0.1:" + b.HostPort
		}
		return ""
	}
	grpcAddr, restAddr := hostPort(grpcPort), hostPort(restPort)
	if grpcAddr == "" || restAddr == "" {
		return nil, errors.New("sandboxd ports were not published")
	}
	return sandboxd.Dial(ctx, grpcAddr, restAddr, d.token, nil)
}

func (d *Driver) removeContainer(ctx context.Context, name string) error {
	_, err := d.cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	return err
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
	c, err := d.createContainer(ctx, name, image)
	if err != nil {
		return nil, false, err
	}
	sb := &liveSandbox{ttl: ttl, lastUsed: time.Now(), client: c}
	d.live[name] = sb
	return sb, true, nil
}

func (d *Driver) lookup(name string) *liveSandbox {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.live[name]
}

func (d *Driver) destroy(name string) error {
	d.mu.Lock()
	sb, ok := d.live[name]
	delete(d.live, name)
	d.mu.Unlock()
	if ok {
		sb.client.Close()
	}
	return d.removeContainer(context.Background(), name)
}

func (d *Driver) reap() {
	for range time.Tick(time.Minute) {
		d.mu.Lock()
		idle := map[string]*liveSandbox{}
		for name, sb := range d.live {
			if sb.mu.TryLock() {
				if time.Since(sb.lastUsed) > sb.ttl {
					idle[name] = sb
				}
				sb.mu.Unlock()
			}
		}
		d.mu.Unlock()
		for name, sb := range idle {
			sb.mu.Lock()
			if time.Since(sb.lastUsed) > sb.ttl {
				_ = d.destroy(name)
			}
			sb.mu.Unlock()
		}
	}
}

// Live reports the running sandboxes and when each last ran a turn.
func (d *Driver) Live(context.Context) map[string]time.Time {
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
		_ = d.destroy(name)
	}
}

// RunTurn executes one turn in the named sandbox; create and connect start it, other turns need it running.
func (d *Driver) RunTurn(ctx context.Context, req engine.TurnRequest) (engine.TurnResponse, error) {
	if req.Fn == engine.DestroyFn {
		if err := d.destroy(req.SandboxName); err != nil {
			return engine.TurnResponse{}, fmt.Errorf("remove container %s: %w", req.SandboxName, err)
		}
		req.Logf("sandbox %s destroyed", req.SandboxName)
		return engine.TurnResponse{Output: json.RawMessage(`{"destroyed":true}`)}, nil
	}
	timings := map[string]int64{}
	start := time.Now()
	var sb *liveSandbox
	var fresh bool
	var err error
	if req.Fn == engine.CreateFn || req.Fn == engine.ConnectFn {
		if sb, fresh, err = d.acquire(ctx, req.SandboxName, req.Image, req.IdleTTL); err != nil {
			return engine.TurnResponse{}, err
		}
	} else if sb = d.lookup(req.SandboxName); sb == nil {
		return engine.TurnResponse{}, fmt.Errorf("sandbox %s is not running on this provider (idle-expired, killed, or started on another provider): connect to it first", req.SandboxID)
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.lastUsed = time.Now()
	defer func() { sb.lastUsed = time.Now() }()
	driver.Phase(timings, "claim", start)
	if fresh {
		req.Logf("sandbox %s created", req.SandboxName)
	} else {
		req.Logf("sandbox %s reused", req.SandboxName)
	}
	return driver.Run(ctx, sb.client, req, fresh, timings, driver.Options{MaxFileBytes: d.opts.MaxFileBytes, Staging: d.opts.Staging})
}
