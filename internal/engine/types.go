package engine

import (
	"context"
	"encoding/json"
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
	Output     json.RawMessage `json:"output"`
	SnapshotID string          `json:"snapshotId"`
}

type TurnRequest struct {
	SandboxName string
	Image       string
	Fn          string
	FnSource    string
	Input       json.RawMessage
	SnapshotTar []byte
}

type TurnResponse struct {
	Output      json.RawMessage
	SnapshotTar []byte
}

type Driver interface {
	RunTurn(ctx context.Context, req TurnRequest) (TurnResponse, error)
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
