// Package driver holds the turn body shared by the Docker and Kubernetes
// drivers: restore, file and command functions, and the checkpoint, all
// through the sandboxd running inside the sandbox.
package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hatchet-dev/brickie/internal/driver/fsops"
	"github.com/hatchet-dev/brickie/internal/engine"
	"github.com/hatchet-dev/brickie/internal/sandboxd"
)

const restoreName = ".brickie-restore.tar.gz"

// Staging is the store that carries files larger than MaxFileBytes.
type Staging = fsops.Staging

// Options are the per-turn limits a driver passes through.
type Options struct {
	MaxFileBytes int64
	Staging      Staging
}

// Phase records how long a named step of a turn took.
func Phase(timings map[string]int64, name string, start time.Time) {
	timings[name] = time.Since(start).Milliseconds()
}

// Run executes one turn on a sandbox that is already up. fresh says the
// workspace is empty and must be restored from the snapshot, if there is one.
func Run(ctx context.Context, c *sandboxd.Client, req engine.TurnRequest, fresh bool, timings map[string]int64, opts Options) (engine.TurnResponse, error) {
	if fresh && req.SnapshotTar != nil {
		start := time.Now()
		if err := c.Write(ctx, restoreName, bytes.NewReader(req.SnapshotTar), int64(len(req.SnapshotTar))); err != nil {
			return engine.TurnResponse{}, fmt.Errorf("restore snapshot: %w", err)
		}
		res, err := c.Run(ctx, []string{"sh", "-c", "tar -xzf " + restoreName + " && rm -f " + restoreName}, 0)
		if err != nil {
			return engine.TurnResponse{}, fmt.Errorf("restore snapshot: %w", err)
		}
		if res.ExitCode != 0 {
			return engine.TurnResponse{}, fmt.Errorf("restore snapshot (exit %d): %s", res.ExitCode, strings.TrimSpace(res.Stderr))
		}
		Phase(timings, "restore", start)
		req.Logf("workspace restored from snapshot %s", req.SnapshotID)
	}
	if req.Fn == engine.CreateFn || req.Fn == engine.ConnectFn {
		out, _ := json.Marshal(map[string]bool{"created": fresh})
		return engine.TurnResponse{Output: out, Timings: timings}, nil
	}

	start := time.Now()
	var output any
	var mutated bool
	var err error
	switch {
	case strings.HasPrefix(req.Fn, fsops.Prefix):
		output, mutated, err = fsops.Turn(ctx, c, req.Fn, req.Input, fsops.Options{MaxBytes: opts.MaxFileBytes, Staging: opts.Staging, SandboxID: req.SandboxID})
		if err != nil {
			return engine.TurnResponse{}, err
		}
	case req.Fn == engine.CommandFn:
		var in engine.CommandInput
		if err := json.Unmarshal(req.Input, &in); err != nil {
			return engine.TurnResponse{}, err
		}
		res, err := c.Run(ctx, []string{"sh", "-c", in.Cmd}, time.Duration(in.TimeoutMs)*time.Millisecond)
		if err != nil {
			return engine.TurnResponse{}, err
		}
		output = engine.CommandOutput{Stdout: strings.TrimSpace(string(res.Stdout)), Stderr: strings.TrimSpace(res.Stderr), ExitCode: res.ExitCode}
		mutated = true
	default:
		return engine.TurnResponse{}, fmt.Errorf("unknown sandbox fn %q", req.Fn)
	}
	Phase(timings, "exec", start)
	req.Logf("fn %s executed in %s", req.Fn, time.Since(start).Round(time.Millisecond))
	outJSON, err := json.Marshal(output)
	if err != nil {
		return engine.TurnResponse{}, err
	}
	if !mutated {
		return engine.TurnResponse{Output: outJSON, Timings: timings}, nil
	}
	start = time.Now()
	res, err := c.Run(ctx, []string{"tar", "-czf", "-", "."}, 0)
	if err != nil {
		return engine.TurnResponse{}, fmt.Errorf("snapshot workspace: %w", err)
	}
	if res.ExitCode != 0 {
		return engine.TurnResponse{}, fmt.Errorf("snapshot workspace (exit %d): %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	Phase(timings, "checkpoint", start)
	return engine.TurnResponse{Output: outJSON, SnapshotTar: res.Stdout, Timings: timings}, nil
}
