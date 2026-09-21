package engine

import (
	"context"
	"fmt"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"github.com/hatchet-dev/brickie/internal/image"
)

type ProviderOpts struct {
	Driver        Driver
	Store         SnapshotStore
	ImageProvider image.Provider
	TurnTimeout   time.Duration
	Retries       int
}

type turnRunner struct {
	opts          ProviderOpts
	imageProvider image.Provider
	timeout       time.Duration
	retries       int
	native        bool
}

func newTurnRunner(opts ProviderOpts) *turnRunner {
	r := &turnRunner{opts: opts, imageProvider: opts.ImageProvider, timeout: opts.TurnTimeout, retries: opts.Retries}
	if r.timeout == 0 {
		r.timeout = 15 * time.Minute
	}
	if r.retries == 0 {
		r.retries = 2
	}
	if r.imageProvider == nil {
		r.imageProvider = image.Registry{}
	}
	if d, ok := opts.Driver.(SnapshotNativeDriver); ok {
		r.native = d.SnapshotNative()
	}
	return r
}

func (r *turnRunner) run(ctx context.Context, runID string, input TurnInput, log func(string)) (TurnResult, error) {
	resolved, err := r.imageProvider.Resolve(ctx, image.Spec{Ref: input.Image})
	if err != nil {
		if log != nil {
			log(fmt.Sprintf("resolve image %q failed: %v", input.Image, err))
		}
		return TurnResult{}, fmt.Errorf("resolve image: %w", err)
	}
	req := TurnRequest{
		SandboxName:  "brickie-" + runID,
		Image:        resolved.Ref,
		Fn:           input.Fn,
		FnSource:     input.FnSource,
		Input:        input.Input,
		SnapshotID:   input.SnapshotID,
		CheckpointID: runID,
		Log:          log,
	}
	req.Logf("turn start: fn=%s image=%s sandbox=%s snapshot=%s", input.Fn, resolved.Ref, req.SandboxName, orNone(input.SnapshotID))
	if !r.native && input.SnapshotID != "" {
		tar, err := r.opts.Store.Read(input.SnapshotID)
		if err != nil {
			req.Logf("read snapshot %s failed: %v", input.SnapshotID, err)
			return TurnResult{}, fmt.Errorf("read snapshot %s: %w", input.SnapshotID, err)
		}
		req.SnapshotTar = tar
		req.Logf("restored snapshot %s (%d bytes)", input.SnapshotID, len(tar))
	}
	started := time.Now()
	resp, err := r.opts.Driver.RunTurn(ctx, req)
	if err != nil {
		req.Logf("turn failed after %s: %v", time.Since(started).Round(time.Millisecond), err)
		return TurnResult{}, err
	}
	req.Logf("driver finished in %s", time.Since(started).Round(time.Millisecond))
	if !r.native {
		if err := r.opts.Store.Write(runID, resp.SnapshotTar); err != nil {
			req.Logf("write snapshot %s failed: %v", runID, err)
			return TurnResult{}, fmt.Errorf("write snapshot %s: %w", runID, err)
		}
		req.Logf("snapshot %s stored (%d bytes)", runID, len(resp.SnapshotTar))
	}
	req.Logf("turn done: snapshot=%s", runID)
	return TurnResult{Output: resp.Output, SnapshotID: runID}, nil
}

func NewProviderTask(client *hatchet.Client, opts ProviderOpts) *hatchet.StandaloneTask {
	runner := newTurnRunner(opts)
	return client.NewStandaloneDurableTask(TaskName, func(ctx hatchet.DurableContext, input TurnInput) (TurnResult, error) {
		return runner.run(ctx, ctx.WorkflowRunId(), input, ctx.Log)
	}, hatchet.WithExecutionTimeout(runner.timeout), hatchet.WithRetries(runner.retries))
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
