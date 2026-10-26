// Package engine defines the sandbox turn contract and the provider that runs it.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// TurnRequest carries one turn to a driver. Snapshot, when set, is the
// workspace tar to restore into a fresh sandbox; Checkpoint receives the tar
// of the workspace after a mutating turn, streamed as it is produced.
type TurnRequest struct {
	SandboxID    string
	SandboxName  string
	Image        string
	Fn           string
	Input        json.RawMessage
	Snapshot     io.Reader
	SnapshotSize int64
	SnapshotID   string
	Checkpoint   io.Writer
	CheckpointID string
	IdleTTL      time.Duration
	Metadata     map[string]string
	Log          func(message string)
}

func (r TurnRequest) Logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(fmt.Sprintf(format, args...))
	}
}

type TurnResponse struct {
	Output      json.RawMessage
	Snapshotted bool
	Timings     map[string]int64
}

// Driver runs turns and reports the sandboxes it has running with the time
// each last ran a turn.
type Driver interface {
	RunTurn(ctx context.Context, req TurnRequest) (TurnResponse, error)
	Live(ctx context.Context) map[string]time.Time
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
