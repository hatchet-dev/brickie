package docker

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/hatchet-dev/brickie/internal/sandboxd"
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
	dest := filepath.Join(cacheDir, "brickie", "sandboxd-"+arch+"-"+sandboxd.Image[len(sandboxd.Image)-12:])
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	platform := ocispec.Platform{OS: "linux", Architecture: arch}
	pull, err := d.cli.ImagePull(ctx, sandboxd.Image, client.ImagePullOptions{Platforms: []ocispec.Platform{platform}})
	if err != nil {
		return "", fmt.Errorf("pull sandboxd: %w", err)
	}
	if err := pull.Wait(ctx); err != nil {
		return "", fmt.Errorf("pull sandboxd: %w", err)
	}
	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:   &container.Config{Image: sandboxd.Image},
		Platform: &platform,
	})
	if err != nil {
		return "", fmt.Errorf("extract sandboxd: %w", err)
	}
	defer func() {
		_, _ = d.cli.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	res, err := d.cli.CopyFromContainer(ctx, created.ID, client.CopyFromContainerOptions{SourcePath: sandboxd.ImageBinary})
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
