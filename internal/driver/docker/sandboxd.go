package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	processv1 "sigs.k8s.io/agent-sandbox/packages/sandboxd/spec/process/v1"
)

// sandboxdImage is pinned by index digest: upstream publishes only a moving
// latest-main tag from its staging registry.
const sandboxdImage = "us-central1-docker.pkg.dev/k8s-staging-images/agent-sandbox/sandboxd@sha256:48390a8888d7abf2b3ba471ecb53cc37e11ed370da15c69ee76881962bc9e634"

const (
	sandboxdPath     = "/sandboxd"
	sandboxdGRPCPort = "9090/tcp"
	sandboxdReady    = 30 * time.Second
)

// ensureSandboxd returns a host path to the sandboxd binary for the engine's
// architecture, extracting it from the pinned image on first use.
func (d *Driver) ensureSandboxd(ctx context.Context) (string, error) {
	info, err := d.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return "", fmt.Errorf("docker info: %w", err)
	}
	arch := map[string]string{"x86_64": "amd64", "aarch64": "arm64"}[info.Info.Architecture]
	if arch == "" {
		arch = info.Info.Architecture
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	dest := filepath.Join(cacheDir, "brickie", "sandboxd-"+arch+"-"+sandboxdImage[len(sandboxdImage)-12:])
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	platform := ocispec.Platform{OS: "linux", Architecture: arch}
	pull, err := d.cli.ImagePull(ctx, sandboxdImage, client.ImagePullOptions{Platforms: []ocispec.Platform{platform}})
	if err != nil {
		return "", fmt.Errorf("pull sandboxd: %w", err)
	}
	if err := pull.Wait(ctx); err != nil {
		return "", fmt.Errorf("pull sandboxd: %w", err)
	}
	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:   &container.Config{Image: sandboxdImage},
		Platform: &platform,
	})
	if err != nil {
		return "", fmt.Errorf("extract sandboxd: %w", err)
	}
	defer func() {
		_, _ = d.cli.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	res, err := d.cli.CopyFromContainer(ctx, created.ID, client.CopyFromContainerOptions{SourcePath: "/usr/local/bin/sandboxd"})
	if err != nil {
		return "", fmt.Errorf("extract sandboxd: %w", err)
	}
	defer func() { _ = res.Content.Close() }()
	tr := tar.NewReader(res.Content)
	for {
		hdr, err := tr.Next()
		if err != nil {
			return "", fmt.Errorf("extract sandboxd: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			return "", err
		}
		tmp := dest + ".tmp"
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755) // #nosec G302 G304 -- the daemon binary must be executable; dest is provider-built
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(f, tr); err != nil { // #nosec G110 -- a single known binary from a pinned image
			_ = f.Close()
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
		return dest, os.Rename(tmp, dest)
	}
}

type sandboxdClient struct {
	conn *grpc.ClientConn
	proc processv1.ProcessServiceClient
}

// connectSandboxd dials the daemon's published gRPC port and waits until it
// answers.
func (d *Driver) connectSandboxd(ctx context.Context, containerID string) (*sandboxdClient, error) {
	inspect, err := d.cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	var hostPort string
	if inspect.Container.NetworkSettings != nil {
		for _, b := range inspect.Container.NetworkSettings.Ports[network.MustParsePort(sandboxdGRPCPort)] {
			hostPort = b.HostPort
		}
	}
	if hostPort == "" {
		return nil, errors.New("sandboxd port was not published")
	}
	conn, err := grpc.NewClient("127.0.0.1:"+hostPort, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	c := &sandboxdClient{conn: conn, proc: processv1.NewProcessServiceClient(conn)}
	deadline := time.Now().Add(sandboxdReady)
	for {
		_, err := c.proc.Execute(ctx, &processv1.ExecuteRequest{Config: &processv1.ProcessConfig{Command: []string{"/bin/sh", "-c", ":"}, Cwd: proto.String(workspacePath)}})
		if err == nil {
			return c, nil
		}
		if status.Code(err) != codes.Unavailable || time.Now().After(deadline) {
			_ = conn.Close()
			return nil, fmt.Errorf("sandboxd did not become ready: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = conn.Close()
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (c *sandboxdClient) close() {
	if c != nil && c.conn != nil {
		_ = c.conn.Close()
	}
}

// run streams a command through the daemon. On timeout the daemon kills the
// whole process group and the result reports exit code 124.
func (c *sandboxdClient) run(ctx context.Context, argv []string, timeout time.Duration) (execResult, error) {
	stream, err := c.proc.Start(ctx, &processv1.StartRequest{Config: &processv1.ProcessConfig{Command: argv, Cwd: proto.String(workspacePath)}})
	if err != nil {
		return execResult{}, fmt.Errorf("start %s: %w", strings.Join(argv, " "), err)
	}
	var stdout, stderr bytes.Buffer
	var timedOut atomic.Bool
	var timer *time.Timer
	code := -1
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if timer != nil {
				timer.Stop()
			}
			return execResult{}, fmt.Errorf("run %s: %w", strings.Join(argv, " "), err)
		}
		switch {
		case ev.GetInit() != nil:
			if timeout > 0 {
				pid := ev.GetInit().GetProcessId()
				timer = time.AfterFunc(timeout, func() {
					timedOut.Store(true)
					kctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					_, _ = c.proc.SendSignal(kctx, &processv1.SendSignalRequest{ProcessId: pid, Signal: processv1.Signal_SIGNAL_SIGKILL})
				})
			}
		case ev.GetExit() != nil:
			code = int(ev.GetExit().GetExitCode())
		default:
			stdout.Write(ev.GetStdout())
			stderr.Write(ev.GetStderr())
		}
		if ev.GetExit() != nil {
			break
		}
	}
	if timer != nil {
		timer.Stop()
	}
	res := execResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: code}
	if timedOut.Load() {
		res.ExitCode = 124
		res.Stderr = strings.TrimSpace(res.Stderr + "\ncommand timed out after " + timeout.String())
	}
	return res, nil
}
