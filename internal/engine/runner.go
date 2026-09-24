package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const Workspace = "/workspace"

const RunnerJS = `
const fs = require('node:fs');
const fsp = require('node:fs/promises');
const path = require('node:path');
const os = require('node:os');
const child_process = require('node:child_process');
(async () => {
  const here = __dirname;
  const workspace = path.dirname(here);
  const { fnSource, input } = JSON.parse(fs.readFileSync(path.join(here, 'input.json'), 'utf8'));
  const fn = eval('(' + fnSource + ')');
  const output = await fn(input, { workspace });
  fs.writeFileSync(path.join(here, 'output.json'), JSON.stringify(output ?? {}));
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`

func RunnerInput(fnSource string, input json.RawMessage) []byte {
	b, _ := json.Marshal(map[string]any{"fnSource": fnSource, "input": input})
	return b
}

// ConversationFile is the workspace-relative transcript file that rides the
// snapshot chain, so resuming a snapshot resumes the conversation.
const ConversationFile = ".conversation.json"

const maxConversationChars = 32000

// ConversationTurn is one user/assistant exchange in the transcript.
type ConversationTurn struct {
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}

// LoadConversation reads the transcript from a workspace, empty when absent.
func LoadConversation(workspaceDir string) []ConversationTurn {
	b, err := os.ReadFile(filepath.Join(workspaceDir, ConversationFile)) // #nosec G304 -- workspaceDir is the provider's own turn directory
	if err != nil {
		return nil
	}
	var turns []ConversationTurn
	if err := json.Unmarshal(b, &turns); err != nil {
		return nil
	}
	return turns
}

// AppendConversation records one exchange, dropping the oldest entries once
// the transcript exceeds the size cap.
func AppendConversation(workspaceDir, user, assistant string) error {
	turns := append(LoadConversation(workspaceDir), ConversationTurn{User: user, Assistant: assistant})
	total := 0
	for i := len(turns) - 1; i >= 0; i-- {
		total += len(turns[i].User) + len(turns[i].Assistant)
		if total > maxConversationChars {
			turns = turns[i+1:]
			break
		}
	}
	b, err := json.Marshal(turns)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(workspaceDir, ConversationFile), b, 0o600)
}

func renderConversation(turns []ConversationTurn) string {
	if len(turns) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Earlier conversation in this session:\n")
	for _, t := range turns {
		sb.WriteString("User: " + t.User + "\nAssistant: " + t.Assistant + "\n")
	}
	sb.WriteString("\nCurrent request: ")
	return sb.String()
}

// PromptCommand builds the claude invocation for one prompt turn, anchored to
// the workspace and carrying the transcript.
func PromptCommand(workspace string, prompt string, history []ConversationTurn) []string {
	return []string{
		"claude",
		"-p",
		fmt.Sprintf("Your workspace is %s — create and modify files only there. %s%s", workspace, renderConversation(history), prompt),
		"--dangerously-skip-permissions",
		"--output-format",
		"json",
	}
}

func ParsePromptReply(stdout string) string {
	reply := strings.TrimSpace(stdout)
	var parsed struct {
		Result  string `json:"result"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal([]byte(reply), &parsed); err == nil {
		if parsed.Result != "" {
			return parsed.Result
		}
		if parsed.Subtype != "" {
			return parsed.Subtype
		}
	}
	return reply
}

func ParsePromptAPIMs(stdout string) int64 {
	var parsed struct {
		DurationAPIMs int64 `json:"duration_api_ms"`
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(stdout)), &parsed)
	return parsed.DurationAPIMs
}

// ExcludedFiles are engine-internal or tooling entries hidden from a prompt
// turn's files listing.
var ExcludedFiles = map[string]bool{".brickie": true, ConversationFile: true, "node_modules": true, "package.json": true, "package-lock.json": true}

func FilterFiles(names []string) []string {
	out := []string{}
	for _, n := range names {
		if n == "" || ExcludedFiles[n] {
			continue
		}
		out = append(out, n)
	}
	return out
}
