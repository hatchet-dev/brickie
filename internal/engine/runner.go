package engine

import (
	"encoding/json"
	"fmt"
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

func PromptCommand(workspace string, prompt string) []string {
	return []string{
		"claude",
		"-p",
		fmt.Sprintf("Your workspace is %s — create and modify files only there. %s", workspace, prompt),
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

// ParsePromptAPIMs extracts the time claude spent in Anthropic API calls from
// its JSON output, 0 when absent.
func ParsePromptAPIMs(stdout string) int64 {
	var parsed struct {
		DurationAPIMs int64 `json:"duration_api_ms"`
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(stdout)), &parsed)
	return parsed.DurationAPIMs
}

var ExcludedFiles = map[string]bool{".brickie": true, "node_modules": true, "package.json": true, "package-lock.json": true}

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
