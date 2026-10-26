package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"gocloud.dev/blob"

	"github.com/hatchet-dev/brickie/internal/image"
)

type ProviderOpts struct {
	Driver           Driver
	Store            *Store
	ImageProvider    image.Provider
	Retries          int
	MaxIdleTTL       time.Duration
	MaxSnapshotBytes int64
}

// turnExecutionTimeout bounds a hung sandbox. The real limits are the SDK's
// per-call timeoutMs and the calling task's own timeout.
const turnExecutionTimeout = 24 * time.Hour

type turnRunner struct {
	opts          ProviderOpts
	imageProvider image.Provider
	retries       int

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newTurnRunner(opts ProviderOpts) *turnRunner {
	r := &turnRunner{opts: opts, imageProvider: opts.ImageProvider, retries: opts.Retries, locks: map[string]*sync.Mutex{}}
	if r.retries == 0 {
		r.retries = 2
	}
	if r.opts.MaxIdleTTL == 0 {
		r.opts.MaxIdleTTL = 24 * time.Hour
	}
	if r.opts.MaxSnapshotBytes == 0 {
		r.opts.MaxSnapshotBytes = 1 << 30
	}
	if r.imageProvider == nil {
		r.imageProvider = image.Registry{}
	}
	return r
}

// lock serializes a sandbox's turns through execution and publication, so
// the latest pointer can never move backwards.
func (r *turnRunner) lock(sandboxID string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	mu, ok := r.locks[sandboxID]
	if !ok {
		mu = &sync.Mutex{}
		r.locks[sandboxID] = mu
	}
	return mu
}

// checkpoint streams a turn's workspace tar into the store, opening the
// object on the first byte so turns that change nothing write nothing.
type checkpoint struct {
	store   *Store
	id      string
	ctx     context.Context
	cancel  context.CancelFunc
	w       *blob.Writer
	limit   int64
	written int64
}

func (c *checkpoint) Write(p []byte) (int, error) {
	if c.w == nil {
		w, err := c.store.NewSnapshotWriter(c.ctx, c.id)
		if err != nil {
			return 0, err
		}
		c.w = w
	}
	if c.written+int64(len(p)) > c.limit {
		return 0, fmt.Errorf("snapshot exceeds the provider limit of %d bytes (--max-snapshot-bytes)", c.limit)
	}
	c.written += int64(len(p))
	return c.w.Write(p)
}

func (c *checkpoint) commit() error {
	if c.w == nil {
		return errors.New("the turn produced an empty snapshot")
	}
	return c.w.Close()
}

func (c *checkpoint) abort() {
	c.cancel()
	if c.w != nil {
		_ = c.w.Close()
	}
}

func (r *turnRunner) publish(ctx context.Context, sandboxID, snapshotID string) error {
	var err error
	for attempt := range 3 {
		if err = r.opts.Store.SetLatest(ctx, sandboxID, snapshotID); err == nil {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
	}
	return fmt.Errorf("record latest snapshot for %s: %w", sandboxID, err)
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
	mu := r.lock(sandboxID)
	mu.Lock()
	defer mu.Unlock()
	if input.SnapshotID != "" {
		rd, err := r.opts.Store.OpenSnapshot(ctx, input.SnapshotID)
		if err != nil {
			req.Logf("read snapshot %s failed: %v", input.SnapshotID, err)
			return TurnResult{}, fmt.Errorf("read snapshot %s: %w", input.SnapshotID, err)
		}
		defer func() { _ = rd.Close() }()
		req.Snapshot, req.SnapshotSize = rd, rd.Size()
		req.Logf("loaded snapshot %s (%d bytes)", input.SnapshotID, rd.Size())
	}
	cp := &checkpoint{store: r.opts.Store, id: runID, limit: r.opts.MaxSnapshotBytes}
	cp.ctx, cp.cancel = context.WithCancel(ctx)
	defer cp.cancel()
	req.Checkpoint = cp
	started := time.Now()
	resp, err := r.opts.Driver.RunTurn(ctx, req)
	if err != nil {
		cp.abort()
		req.Logf("turn failed after %s: %v", time.Since(started).Round(time.Millisecond), err)
		return TurnResult{}, err
	}
	req.Logf("driver finished in %s", time.Since(started).Round(time.Millisecond))
	if input.Fn == CreateFn {
		if err := r.opts.Store.SetMeta(ctx, meta); err != nil {
			return TurnResult{}, fmt.Errorf("record sandbox %s: %w", sandboxID, err)
		}
		if input.SnapshotID != "" {
			if err := r.publish(ctx, sandboxID, input.SnapshotID); err != nil {
				return TurnResult{}, err
			}
		}
	}
	if !resp.Snapshotted {
		cp.abort()
		req.Logf("turn done: no snapshot")
		return TurnResult{Output: resp.Output, SandboxID: sandboxID, Timings: resp.Timings}, nil
	}
	if err := cp.commit(); err != nil {
		req.Logf("write snapshot %s failed: %v", runID, err)
		return TurnResult{}, fmt.Errorf("write snapshot %s: %w", runID, err)
	}
	req.Logf("snapshot %s stored (%d bytes)", runID, cp.written)
	if err := r.publish(ctx, sandboxID, runID); err != nil {
		req.Logf("%v", err)
		return TurnResult{}, err
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
		if IsNotFound(err) {
			return "", SandboxMeta{}, fmt.Errorf("unknown sandbox %s: it was not created on this provider", id)
		}
		if err != nil {
			return "", SandboxMeta{}, fmt.Errorf("read sandbox %s: %w", id, err)
		}
		latest, err := r.opts.Store.Latest(ctx, id)
		if err != nil && !IsNotFound(err) {
			return "", SandboxMeta{}, fmt.Errorf("read latest snapshot of %s: %w", id, err)
		}
		input.SnapshotID = latest
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
