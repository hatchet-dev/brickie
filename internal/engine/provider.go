package engine

import (
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

func NewProviderTask(client *hatchet.Client, opts ProviderOpts) *hatchet.StandaloneTask {
	timeout := opts.TurnTimeout
	if timeout == 0 {
		timeout = 15 * time.Minute
	}
	retries := opts.Retries
	if retries == 0 {
		retries = 2
	}
	native := false
	if d, ok := opts.Driver.(SnapshotNativeDriver); ok {
		native = d.SnapshotNative()
	}
	imageProvider := opts.ImageProvider
	if imageProvider == nil {
		imageProvider = image.Registry{}
	}
	return client.NewStandaloneDurableTask(TaskName, func(ctx hatchet.DurableContext, input TurnInput) (TurnResult, error) {
		runID := ctx.WorkflowRunId()
		resolved, err := imageProvider.Resolve(ctx, image.Spec{Ref: input.Image})
		if err != nil {
			ctx.Log(fmt.Sprintf("resolve image %q failed: %v", input.Image, err))
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
			Log:          ctx.Log,
		}
		req.Logf("turn start: fn=%s image=%s sandbox=%s snapshot=%s", input.Fn, resolved.Ref, req.SandboxName, orNone(input.SnapshotID))
		if !native && input.SnapshotID != "" {
			tar, err := opts.Store.Read(input.SnapshotID)
			if err != nil {
				req.Logf("read snapshot %s failed: %v", input.SnapshotID, err)
				return TurnResult{}, fmt.Errorf("read snapshot %s: %w", input.SnapshotID, err)
			}
			req.SnapshotTar = tar
			req.Logf("restored snapshot %s (%d bytes)", input.SnapshotID, len(tar))
		}
		started := time.Now()
		resp, err := opts.Driver.RunTurn(ctx, req)
		if err != nil {
			req.Logf("turn failed after %s: %v", time.Since(started).Round(time.Millisecond), err)
			return TurnResult{}, err
		}
		req.Logf("driver finished in %s", time.Since(started).Round(time.Millisecond))
		if !native {
			if err := opts.Store.Write(runID, resp.SnapshotTar); err != nil {
				req.Logf("write snapshot %s failed: %v", runID, err)
				return TurnResult{}, fmt.Errorf("write snapshot %s: %w", runID, err)
			}
			req.Logf("snapshot %s stored (%d bytes)", runID, len(resp.SnapshotTar))
		}
		req.Logf("turn done: snapshot=%s", runID)
		return TurnResult{Output: resp.Output, SnapshotID: runID}, nil
	}, hatchet.WithExecutionTimeout(timeout), hatchet.WithRetries(retries))
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
