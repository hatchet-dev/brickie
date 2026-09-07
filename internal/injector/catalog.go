package injector

import (
	"encoding/json"
	"fmt"
	"strings"
)

// CatalogEntry describes how a well-known credential env var routes through
// the injector: which env var the sealed blob lands in, which upstream hosts
// the secret may be used against, and which base-URL env var points the SDK
// at the injector's /t/<host> form. Credentials without a BaseURLEnv cannot
// be routed through the injector by an unmodified SDK and are injected raw.
type CatalogEntry struct {
	TargetEnv   string
	Hosts       []string
	PrimaryHost string
	BaseURLEnv  string
}

var anthropicEntry = CatalogEntry{
	TargetEnv:   "ANTHROPIC_AUTH_TOKEN",
	Hosts:       []string{"api.anthropic.com"},
	PrimaryHost: "api.anthropic.com",
	BaseURLEnv:  "ANTHROPIC_BASE_URL",
}

var geminiEntry = CatalogEntry{
	TargetEnv:   "GEMINI_API_KEY",
	Hosts:       []string{"generativelanguage.googleapis.com"},
	PrimaryHost: "generativelanguage.googleapis.com",
	BaseURLEnv:  "GOOGLE_GEMINI_BASE_URL",
}

// Catalog holds credentials whose SDKs confidently honor a base-URL env var:
// Claude Code and the Agent SDK (ANTHROPIC_BASE_URL), Codex and the OpenAI
// SDKs (OPENAI_BASE_URL), Gemini CLI and google-genai
// (GOOGLE_GEMINI_BASE_URL), Groq (GROQ_BASE_URL), Cohere (CO_API_URL), and
// Hugging Face (HF_ENDPOINT). Providers whose SDKs only take a base URL in
// code fall back to raw injection; add them per deployment with
// ExtendCatalog (BRICKIE_INJECTOR_CATALOG) when the harness honors an
// override.
var Catalog = map[string]CatalogEntry{
	"ANTHROPIC_API_KEY":       anthropicEntry,
	"ANTHROPIC_AUTH_TOKEN":    anthropicEntry,
	"CLAUDE_CODE_OAUTH_TOKEN": anthropicEntry,
	"OPENAI_API_KEY": {
		TargetEnv:   "OPENAI_API_KEY",
		Hosts:       []string{"api.openai.com"},
		PrimaryHost: "api.openai.com",
		BaseURLEnv:  "OPENAI_BASE_URL",
	},
	"GEMINI_API_KEY": geminiEntry,
	"GOOGLE_API_KEY": geminiEntry,
	"GROQ_API_KEY": {
		TargetEnv:   "GROQ_API_KEY",
		Hosts:       []string{"api.groq.com"},
		PrimaryHost: "api.groq.com",
		BaseURLEnv:  "GROQ_BASE_URL",
	},
	"COHERE_API_KEY": {
		TargetEnv:   "COHERE_API_KEY",
		Hosts:       []string{"api.cohere.com"},
		PrimaryHost: "api.cohere.com",
		BaseURLEnv:  "CO_API_URL",
	},
	"HF_TOKEN": {
		TargetEnv:   "HF_TOKEN",
		Hosts:       []string{"huggingface.co", "*.huggingface.co"},
		PrimaryHost: "huggingface.co",
		BaseURLEnv:  "HF_ENDPOINT",
	},
}

// ExtendCatalog merges operator-supplied entries (JSON: env var name →
// entry) into the catalog, overriding defaults on collision. Keys of the
// entry objects: targetEnv, hosts, primaryHost, baseUrlEnv.
func ExtendCatalog(jsonSpec string) error {
	if jsonSpec == "" {
		return nil
	}
	var extra map[string]struct {
		TargetEnv   string   `json:"targetEnv"`
		Hosts       []string `json:"hosts"`
		PrimaryHost string   `json:"primaryHost"`
		BaseURLEnv  string   `json:"baseUrlEnv"`
	}
	if err := json.Unmarshal([]byte(jsonSpec), &extra); err != nil {
		return fmt.Errorf("parse injector catalog extensions: %w", err)
	}
	for name, e := range extra {
		entry := CatalogEntry{TargetEnv: e.TargetEnv, Hosts: e.Hosts, PrimaryHost: e.PrimaryHost, BaseURLEnv: e.BaseURLEnv}
		if entry.TargetEnv == "" {
			entry.TargetEnv = name
		}
		if entry.PrimaryHost == "" && len(entry.Hosts) > 0 {
			entry.PrimaryHost = strings.TrimPrefix(entry.Hosts[0], "*.")
		}
		if len(entry.Hosts) == 0 {
			return fmt.Errorf("injector catalog entry %s must list hosts", name)
		}
		Catalog[name] = entry
	}
	return nil
}
