package injector

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
	"HF_TOKEN": {
		TargetEnv:   "HF_TOKEN",
		Hosts:       []string{"huggingface.co", "*.huggingface.co"},
		PrimaryHost: "huggingface.co",
		BaseURLEnv:  "HF_ENDPOINT",
	},
}
