// Package engine defines the sandbox turn contract and the provider that runs it.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// TaskName is the Hatchet task every SDK call runs.
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

// TurnInput is the task input the SDK sends.
type TurnInput struct {
	SandboxID  string            `json:"sandboxId,omitempty"`
	Image      string            `json:"image,omitempty"`
	Fn         string            `json:"fn"`
	Input      json.RawMessage   `json:"input"`
	SnapshotID string            `json:"snapshotId,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	IdleTTLMs  int64             `json:"idleTtlMs,omitempty"`
}

// TurnResult is the task output the SDK reads.
type TurnResult struct {
	Output     json.RawMessage  `json:"output"`
	SandboxID  string           `json:"sandboxId"`
	SnapshotID string           `json:"snapshotId,omitempty"`
	Timings    map[string]int64 `json:"timings,omitempty"`
}

// TurnRequest is one turn as the driver sees it.
type TurnRequest struct {
	SandboxID    string
	SandboxName  string
	Image        string
	Fn           string
	Input        json.RawMessage
	SnapshotTar  []byte
	SnapshotID   string
	CheckpointID string
	IdleTTL      time.Duration
	Metadata     map[string]string
	Log          func(message string)
}

// Logf writes to the turn log when there is one.
func (r TurnRequest) Logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(fmt.Sprintf(format, args...))
	}
}

// TurnResponse is what a driver returns from a turn.
type TurnResponse struct {
	Output      json.RawMessage
	SnapshotTar []byte
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

// CommandInput is the input of a $command turn.
type CommandInput struct {
	Cmd       string `json:"cmd"`
	TimeoutMs int64  `json:"timeoutMs,omitempty"`
}

// CommandOutput is the output of a $command turn.
type CommandOutput struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}
