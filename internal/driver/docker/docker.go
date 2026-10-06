// Package docker hosts sandboxes as plain containers on a Docker engine.
package docker

import (
	"bytes"
	"context"
	"fmt"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/hatchet-dev/brickie/internal/driver/fsops"
)

const (
	workspacePath = "/workspace"
	label         = "brickie.hatchet.run/sandbox"
)

// Backend runs sandboxes as containers on the Docker engine from the environment.
type Backend struct {
	cli *client.Client
}

// New connects to the engine and checks it answers.
func New() (*Backend, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("docker: %w", err)
	}
	if _, err := cli.Ping(context.Background(), client.PingOptions{}); err != nil {
		return nil, fmt.Errorf("docker engine is not reachable (is Docker running?): %w", err)
	}
	return &Backend{cli: cli}, nil
}

// Name is the driver name shown in logs.
func (b *Backend) Name() string { return "docker" }

// Prune removes containers left by a previous provider process.
func (b *Backend) Prune(ctx context.Context) {
	list, err := b.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", label),
	})
	if err != nil {
		return
	}
	for _, c := range list.Items {
		_, _ = b.cli.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true})
	}
}

func (b *Backend) ensureImage(ctx context.Context, image string) error {
	if _, err := b.cli.ImageInspect(ctx, image); err == nil || !cerrdefs.IsNotFound(err) {
		return err
	}
	pull, err := b.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	if err := pull.Wait(ctx); err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	return nil
}

// Create starts a container with the workspace bind-mounted at /workspace.
func (b *Backend) Create(ctx context.Context, name, image, workspace string) error {
	if err := b.ensureImage(ctx, image); err != nil {
		return err
	}
	b.Destroy(ctx, name)
	created, err := b.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:      image,
			Cmd:        []string{"sleep", "infinity"},
			WorkingDir: workspacePath,
			Labels:     map[string]string{label: "true"},
		},
		HostConfig: &container.HostConfig{
			Mounts: []mount.Mount{{Type: mount.TypeBind, Source: workspace, Target: workspacePath}},
		},
	})
	if err != nil {
		return fmt.Errorf("create container %s: %w", name, err)
	}
	if _, err := b.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start container %s: %w", name, err)
	}
	return nil
}

// Exec runs argv in the container's workspace and returns its output and exit code.
func (b *Backend) Exec(ctx context.Context, name, _ string, argv []string) (fsops.Result, error) {
	ex, err := b.cli.ExecCreate(ctx, name, client.ExecCreateOptions{
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   workspacePath,
		Cmd:          argv,
	})
	if err != nil {
		return fsops.Result{}, fmt.Errorf("exec in %s: %w", name, err)
	}
	attach, err := b.cli.ExecAttach(ctx, ex.ID, client.ExecAttachOptions{})
	if err != nil {
		return fsops.Result{}, fmt.Errorf("exec in %s: %w", name, err)
	}
	var stdout, stderr bytes.Buffer
	_, copyErr := stdcopy.StdCopy(&stdout, &stderr, attach.Reader)
	attach.Close()
	if copyErr != nil {
		return fsops.Result{}, fmt.Errorf("exec in %s: %w", name, copyErr)
	}
	info, err := b.cli.ExecInspect(ctx, ex.ID, client.ExecInspectOptions{})
	if err != nil {
		return fsops.Result{}, fmt.Errorf("exec in %s: %w", name, err)
	}
	return fsops.Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: info.ExitCode}, nil
}

// Destroy force-removes the container.
func (b *Backend) Destroy(ctx context.Context, name string) {
	_, _ = b.cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true})
}
