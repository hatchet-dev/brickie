package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
)

const TaskName = "brickie-sandbox"

const (
	commandFn = "$command"
	promptFn  = "$prompt"
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

type CommandOutput struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

type PromptOutput struct {
	Reply string   `json:"reply"`
	Files []string `json:"files"`
}

type Options struct {
	Image string
}

type ExecOptions struct {
	From string
	Key  string
}

type Item struct {
	Command string
	Prompt  string
	From    string
}

type Sandbox struct {
	client *hatchet.Client
	image  string
	mu     sync.Mutex
	seq    map[string]int
}

var (
	sidecarOnce sync.Once
	sidecar     *exec.Cmd
)

func ensureLocalProvider() {
	if os.Getenv("HATCHET_CLIENT_TOKEN") != "" {
		return
	}
	sidecarOnce.Do(func() {
		bin := os.Getenv("BRICKIE_BIN")
		if bin == "" {
			bin = "brickie"
		}
		sidecar = exec.Command(bin, "worker", "--driver", "local")
		sidecar.Stdout = os.Stderr
		sidecar.Stderr = os.Stderr
		sidecar.Env = os.Environ()
		if err := sidecar.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "brickie: failed to start local sandbox provider (%s): %v\ninstall it with: go install github.com/hatchet-dev/brickie/cmd/brickie@latest (or set BRICKIE_BIN)\n", bin, err)
		}
	})
}

func New(client *hatchet.Client, opts Options) *Sandbox {
	ensureLocalProvider()
	return &Sandbox{client: client, image: opts.Image, seq: map[string]int{}}
}

func (s *Sandbox) nextKey(ctx hatchet.DurableContext) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	runID := ctx.WorkflowRunId()
	n := s.seq[runID]
	s.seq[runID] = n + 1
	return fmt.Sprintf("sbx-%d", n)
}

func (s *Sandbox) turn(fn string, input any, from string) (TurnInput, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return TurnInput{}, err
	}
	return TurnInput{Image: s.image, Fn: fn, Input: raw, SnapshotID: from}, nil
}

func (s *Sandbox) spawn(ctx hatchet.DurableContext, in TurnInput, key string) (*TurnResult, error) {
	if key == "" {
		key = s.nextKey(ctx)
	}
	res, err := s.client.Run(ctx, TaskName, in, hatchet.WithRunKey(key))
	if err != nil {
		return nil, err
	}
	return decode(res)
}

func decode(res *hatchet.WorkflowResult) (*TurnResult, error) {
	raw, err := json.Marshal(res.Raw())
	if err != nil {
		return nil, err
	}
	var byTask map[string]json.RawMessage
	if err := json.Unmarshal(raw, &byTask); err == nil {
		if inner, ok := byTask[TaskName]; ok {
			raw = inner
		}
	}
	var out TurnResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode sandbox turn result: %w", err)
	}
	return &out, nil
}

func (s *Sandbox) ExecuteCommand(ctx hatchet.DurableContext, command string, opts ...ExecOptions) (*TurnResult, error) {
	o := first(opts)
	in, err := s.turn(commandFn, map[string]string{"cmd": command}, o.From)
	if err != nil {
		return nil, err
	}
	return s.spawn(ctx, in, o.Key)
}

func (s *Sandbox) ExecutePrompt(ctx hatchet.DurableContext, prompt string, opts ...ExecOptions) (*TurnResult, error) {
	o := first(opts)
	in, err := s.turn(promptFn, map[string]string{"prompt": prompt}, o.From)
	if err != nil {
		return nil, err
	}
	return s.spawn(ctx, in, o.Key)
}

func (s *Sandbox) ExecuteAll(ctx hatchet.DurableContext, items []Item) ([]TurnResult, error) {
	runs := make([]hatchet.RunManyOpt, 0, len(items))
	for _, item := range items {
		var in TurnInput
		var err error
		switch {
		case item.Command != "":
			in, err = s.turn(commandFn, map[string]string{"cmd": item.Command}, item.From)
		case item.Prompt != "":
			in, err = s.turn(promptFn, map[string]string{"prompt": item.Prompt}, item.From)
		default:
			err = fmt.Errorf("item must set Command or Prompt")
		}
		if err != nil {
			return nil, err
		}
		runs = append(runs, hatchet.RunManyOpt{Input: in, Opts: []hatchet.RunOptFunc{hatchet.WithRunKey(s.nextKey(ctx))}})
	}
	refs, err := s.client.RunMany(ctx, TaskName, runs)
	if err != nil {
		return nil, err
	}
	results := make([]TurnResult, 0, len(refs))
	for i := range refs {
		res, err := refs[i].Result()
		if err != nil {
			return nil, err
		}
		out, err := decode(res)
		if err != nil {
			return nil, err
		}
		results = append(results, *out)
	}
	return results, nil
}

func (r *TurnResult) Command() (CommandOutput, error) {
	var out CommandOutput
	err := json.Unmarshal(r.Output, &out)
	return out, err
}

func (r *TurnResult) Prompt() (PromptOutput, error) {
	var out PromptOutput
	err := json.Unmarshal(r.Output, &out)
	return out, err
}

func first(opts []ExecOptions) ExecOptions {
	if len(opts) > 0 {
		return opts[0]
	}
	return ExecOptions{}
}
