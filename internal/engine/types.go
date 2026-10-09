package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const TaskName = "brickie-sandbox"

// Turn functions the SDK can request. Create, Connect, and Destroy manage the
// sandbox lifecycle; Command runs inside it. Drivers also serve the $fs.* file
// functions.
const (
	CreateFn  = "$create"
	ConnectFn = "$connect"
	ListFn    = "$list"
	PresignFn = "$fs.presign"
	CommandFn = "$command"
	DestroyFn = "$destroy"
)

type TurnInput struct {
	SandboxID  string            `json:"sandboxId,omitempty"`
	Image      string            `json:"image,omitempty"`
	Fn         string            `json:"fn"`
	Input      json.RawMessage   `json:"input"`
	SnapshotID string            `json:"snapshotId,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	IdleTTLMs  int64             `json:"idleTtlMs,omitempty"`
}

type TurnResult struct {
	Output     json.RawMessage  `json:"output"`
	SandboxID  string           `json:"sandboxId"`
	SnapshotID string           `json:"snapshotId,omitempty"`
	Timings    map[string]int64 `json:"timings,omitempty"`
}

type TurnRequest struct {
	SandboxName  string
	Image        string
	Fn           string
	Input        json.RawMessage
	SnapshotTar  []byte
	SnapshotID   string
	CheckpointID string
	IdleTTL      time.Duration
	Log          func(message string)
}

func (r TurnRequest) Logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(fmt.Sprintf(format, args...))
	}
}

type TurnResponse struct {
	Output      json.RawMessage
	SnapshotTar []byte
	Timings     map[string]int64
}

type Driver interface {
	RunTurn(ctx context.Context, req TurnRequest) (TurnResponse, error)
}

// SnapshotNativeDriver marks drivers that restore and checkpoint snapshots
// themselves (overlayfs chains through the CSI node). The provider then passes
// snapshot ids instead of tar bytes and skips its own store entirely.
type SnapshotNativeDriver interface {
	SnapshotNative() bool
}

// LiveLister reports the sandboxes a driver currently has running and when
// each last ran a turn.
type LiveLister interface {
	Live() map[string]time.Time
}

// SandboxMeta is what a sandbox was created with, kept for connect and list.
type SandboxMeta struct {
	SandboxID string            `json:"sandboxId"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	IdleTTL   time.Duration     `json:"idleTtl,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
}

// ListInput filters a $list turn.
type ListInput struct {
	SandboxID string            `json:"sandboxId,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// SandboxInfo is one entry in a $list result.
type SandboxInfo struct {
	SandboxID      string            `json:"sandboxId"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Live           bool              `json:"live"`
	LatestSnapshot string            `json:"latestSnapshot,omitempty"`
	CreatedAt      time.Time         `json:"createdAt"`
	LastUsed       *time.Time        `json:"lastUsed,omitempty"`
}

type CommandInput struct {
	Cmd       string `json:"cmd"`
	TimeoutMs int64  `json:"timeoutMs,omitempty"`
}

type CommandOutput struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}
