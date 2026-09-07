// Package image resolves a sandbox image spec to a concrete image reference
// before a driver launches a sandbox. v1 resolves a pre-built container
// registry reference as-is; the Provider seam lets other strategies
// (template snapshots, BuildKit builds, rootfs-as-layer-chain) plug in later
// without changing drivers, which always receive a resolved ref.
package image

import (
	"context"
	"errors"
)

// Spec describes the image a turn wants. Exactly one field is set. Ref is a
// pre-built registry reference. Build is a declarative build, resolved by a
// provider that can produce an image (not the registry provider).
type Spec struct {
	Ref   string     `json:"ref,omitempty"`
	Build *BuildSpec `json:"build,omitempty"`
}

// BuildSpec is a declarative image build. Its shape will grow as build
// providers land; today no provider consumes it.
type BuildSpec struct {
	Base  string            `json:"base,omitempty"`
	Steps []string          `json:"steps,omitempty"`
	Args  map[string]string `json:"args,omitempty"`
}

// Resolved is the concrete image a driver launches.
type Resolved struct {
	Ref string
}

// Provider turns a Spec into a concrete image reference.
type Provider interface {
	Resolve(ctx context.Context, spec Spec) (Resolved, error)
}

// Registry resolves a pre-built container registry reference. It is the v1
// provider and the default. Default is used when a turn names no image.
type Registry struct {
	Default string
}

var _ Provider = (*Registry)(nil)

func (r Registry) Resolve(_ context.Context, spec Spec) (Resolved, error) {
	if spec.Build != nil {
		return Resolved{}, errors.New("the registry image provider only accepts a pre-built image ref, not a build spec")
	}
	ref := spec.Ref
	if ref == "" {
		ref = r.Default
	}
	return Resolved{Ref: ref}, nil
}
