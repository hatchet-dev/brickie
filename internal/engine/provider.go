package engine

import (
	"fmt"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
)

type ProviderOpts struct {
	Driver      Driver
	Store       SnapshotStore
	TurnTimeout time.Duration
	Retries     int
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
	return client.NewStandaloneDurableTask(TaskName, func(ctx hatchet.DurableContext, input TurnInput) (TurnResult, error) {
		runID := ctx.WorkflowRunId()
		req := TurnRequest{
			SandboxName:  "bx-" + runID,
			Image:        input.Image,
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
