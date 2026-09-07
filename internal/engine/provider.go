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
		}
		if !native && input.SnapshotID != "" {
			tar, err := opts.Store.Read(input.SnapshotID)
			if err != nil {
				return TurnResult{}, fmt.Errorf("read snapshot %s: %w", input.SnapshotID, err)
			}
			req.SnapshotTar = tar
		}
		resp, err := opts.Driver.RunTurn(ctx, req)
		if err != nil {
			return TurnResult{}, err
		}
		if !native {
			if err := opts.Store.Write(runID, resp.SnapshotTar); err != nil {
				return TurnResult{}, fmt.Errorf("write snapshot %s: %w", runID, err)
			}
		}
		return TurnResult{Output: resp.Output, SnapshotID: runID}, nil
	}, hatchet.WithExecutionTimeout(timeout), hatchet.WithRetries(retries))
}
