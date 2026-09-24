package engine

import (
	"context"
	"encoding/json"
	"fmt"
)

const TaskName = "brickie-sandbox"

const (
	CommandFn = "$command"
	PromptFn  = "$prompt"
)

type TurnInput struct {
	Image      string          `json:"image,omitempty"`
	Fn         string          `json:"fn"`
	FnSource   string          `json:"fnSource,omitempty"`
	Input      json.RawMessage `json:"input"`
	SnapshotID string          `json:"snapshotId,omitempty"`
}

type TurnResult struct {
	Output     json.RawMessage  `json:"output"`
	SnapshotID string           `json:"snapshotId"`
	Timings    map[string]int64 `json:"timings,omitempty"`
}

type TurnRequest struct {
	SandboxName  string
	Image        string
	Fn           string
	FnSource     string
	Input        json.RawMessage
	SnapshotTar  []byte
	SnapshotID   string
	CheckpointID string
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

type CommandInput struct {
	Cmd string `json:"cmd"`
}

type CommandOutput struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

type PromptInput struct {
	Prompt string `json:"prompt"`
}

type PromptOutput struct {
	Reply string   `json:"reply"`
	Files []string `json:"files"`
}
