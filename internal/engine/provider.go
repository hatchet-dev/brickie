package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/hatchet-dev/brickie/internal/image"
)

// ProviderOpts wires a driver and a store into the provider.
type ProviderOpts struct {
	Driver        Driver
	Store         *Store
	ImageProvider image.Provider
	Retries       int
	MaxIdleTTL    time.Duration
}

// turnExecutionTimeout bounds a hung sandbox. The real limits are the SDK's
// per-call timeoutMs and the calling task's own timeout.
const turnExecutionTimeout = 24 * time.Hour

type turnRunner struct {
	opts          ProviderOpts
	imageProvider image.Provider
	retries       int
}

func newTurnRunner(opts ProviderOpts) *turnRunner {
	r := &turnRunner{opts: opts, imageProvider: opts.ImageProvider, retries: opts.Retries}
	if r.retries == 0 {
		r.retries = 2
	}
	if r.opts.MaxIdleTTL == 0 {
		r.opts.MaxIdleTTL = 24 * time.Hour
	}
	if r.imageProvider == nil {
		r.imageProvider = image.Registry{}
	}
	return r
}

func (r *turnRunner) run(ctx context.Context, runID string, input TurnInput, log func(string)) (TurnResult, error) {
	if input.Fn == ListFn {
		return r.list(ctx, input)
	}
	resolved, err := r.imageProvider.Resolve(ctx, image.Spec{Ref: input.Image})
	if err != nil {
		if log != nil {
			log(fmt.Sprintf("resolve image %q failed: %v", input.Image, err))
		}
		return TurnResult{}, fmt.Errorf("resolve image: %w", err)
	}
	sandboxID, meta, err := r.resolveSandbox(ctx, &input, runID)
	if err != nil {
		return TurnResult{}, err
	}
	if input.Fn == PresignFn {
		return r.presign(ctx, sandboxID)
	}
	req := TurnRequest{
		SandboxID:    sandboxID,
		SandboxName:  "brickie-" + sandboxID,
		Image:        resolved.Ref,
		Fn:           input.Fn,
		Input:        input.Input,
		SnapshotID:   input.SnapshotID,
		CheckpointID: runID,
		IdleTTL:      meta.IdleTTL,
		Metadata:     meta.Metadata,
		Log:          log,
	}
	req.Logf("turn start: fn=%s image=%s sandbox=%s snapshot=%s", input.Fn, resolved.Ref, req.SandboxName, orNone(input.SnapshotID))
	if input.SnapshotID != "" {
		tar, err := r.opts.Store.ReadSnapshot(ctx, input.SnapshotID)
		if err != nil {
			req.Logf("read snapshot %s failed: %v", input.SnapshotID, err)
			return TurnResult{}, fmt.Errorf("read snapshot %s: %w", input.SnapshotID, err)
		}
		req.SnapshotTar = tar
		req.Logf("loaded snapshot %s (%d bytes)", input.SnapshotID, len(tar))
	}
	started := time.Now()
	resp, err := r.opts.Driver.RunTurn(ctx, req)
	if err != nil {
		req.Logf("turn failed after %s: %v", time.Since(started).Round(time.Millisecond), err)
		return TurnResult{}, err
	}
	req.Logf("driver finished in %s", time.Since(started).Round(time.Millisecond))
	if input.Fn == CreateFn {
		if err := r.opts.Store.SetMeta(ctx, meta); err != nil {
			return TurnResult{}, fmt.Errorf("record sandbox %s: %w", sandboxID, err)
		}
	}
	if resp.SnapshotTar == nil {
		req.Logf("turn done: no snapshot")
		return TurnResult{Output: resp.Output, SandboxID: sandboxID, Timings: resp.Timings}, nil
	}
	if err := r.opts.Store.WriteSnapshot(ctx, runID, resp.SnapshotTar); err != nil {
		req.Logf("write snapshot %s failed: %v", runID, err)
		return TurnResult{}, fmt.Errorf("write snapshot %s: %w", runID, err)
	}
	req.Logf("snapshot %s stored (%d bytes)", runID, len(resp.SnapshotTar))
	if err := r.opts.Store.SetLatest(ctx, sandboxID, runID); err != nil {
		req.Logf("record latest snapshot for %s failed: %v", sandboxID, err)
	}
	req.Logf("turn done: snapshot=%s", runID)
	return TurnResult{Output: resp.Output, SandboxID: sandboxID, SnapshotID: runID, Timings: resp.Timings}, nil
}

var sandboxIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func (r *turnRunner) resolveSandbox(ctx context.Context, input *TurnInput, runID string) (string, SandboxMeta, error) {
	id := input.SandboxID
	switch input.Fn {
	case CreateFn:
		if id == "" {
			id = runID
		} else if !sandboxIDRe.MatchString(id) {
			return "", SandboxMeta{}, fmt.Errorf("invalid sandbox id %q: use lowercase letters, digits, and hyphens, up to 63 characters", id)
		} else if _, err := r.opts.Store.Meta(ctx, id); err == nil {
			return "", SandboxMeta{}, fmt.Errorf("sandbox %s already exists: use connect to continue it, or a new id", id)
		}
		ttl := time.Duration(input.IdleTTLMs) * time.Millisecond
		if ttl > r.opts.MaxIdleTTL {
			return "", SandboxMeta{}, fmt.Errorf("idle ttl %s exceeds the provider maximum %s", ttl, r.opts.MaxIdleTTL)
		}
		return id, SandboxMeta{SandboxID: id, Metadata: input.Metadata, IdleTTL: ttl, CreatedAt: time.Now()}, nil
	case ConnectFn:
		if id == "" {
			return "", SandboxMeta{}, errors.New("connect needs a sandbox id")
		}
		meta, err := r.opts.Store.Meta(ctx, id)
		if err != nil {
			return "", SandboxMeta{}, fmt.Errorf("unknown sandbox %s: it was not created on this provider", id)
		}
		if latest, err := r.opts.Store.Latest(ctx, id); err == nil {
			input.SnapshotID = latest
		}
		return id, meta, nil
	default:
		if id == "" {
			return "", SandboxMeta{}, errors.New("sandboxId is required: create or connect the sandbox first")
		}
		meta, _ := r.opts.Store.Meta(ctx, id)
		return id, meta, nil
	}
}

func (r *turnRunner) list(ctx context.Context, input TurnInput) (TurnResult, error) {
	var in ListInput
	if len(input.Input) > 0 {
		if err := json.Unmarshal(input.Input, &in); err != nil {
			return TurnResult{}, err
		}
	}
	metas, err := r.opts.Store.ListMeta(ctx)
	if err != nil {
		return TurnResult{}, err
	}
	live := r.opts.Driver.Live(ctx)
	infos := []SandboxInfo{}
	for _, m := range metas {
		if (in.SandboxID != "" && m.SandboxID != in.SandboxID) || !matches(m.Metadata, in.Metadata) {
			continue
		}
		info := SandboxInfo{SandboxID: m.SandboxID, Metadata: m.Metadata, CreatedAt: m.CreatedAt}
		info.LatestSnapshot, _ = r.opts.Store.Latest(ctx, m.SandboxID)
		if t, ok := live["brickie-"+m.SandboxID]; ok {
			info.Live = true
			info.LastUsed = &t
		}
		infos = append(infos, info)
	}
	out, err := json.Marshal(map[string]any{"sandboxes": infos})
	if err != nil {
		return TurnResult{}, err
	}
	return TurnResult{Output: out}, nil
}

func (r *turnRunner) presign(ctx context.Context, sandboxID string) (TurnResult, error) {
	key := r.opts.Store.NewKey(sandboxID)
	putURL, getURL, err := r.opts.Store.Presign(ctx, key)
	if err != nil {
		return TurnResult{}, fmt.Errorf("presign staging key: %w", err)
	}
	b, err := json.Marshal(map[string]any{"stagedKey": key, "putUrl": putURL, "getUrl": getURL, "expiresInMs": stagingURLExpiry.Milliseconds()})
	if err != nil {
		return TurnResult{}, err
	}
	return TurnResult{Output: b, SandboxID: sandboxID}, nil
}

func matches(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
