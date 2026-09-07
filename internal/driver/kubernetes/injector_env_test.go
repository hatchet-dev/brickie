package kubernetes

import (
	"strings"
	"testing"

	"github.com/hatchet-dev/brickie/internal/injector"
)

func TestBuildInjectorEnv(t *testing.T) {
	pub, priv, err := injector.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-real",
		"MY_CUSTOM_SECRET":  "raw-value",
	}
	env, err := buildInjectorEnv(pub, 3128, []string{"ANTHROPIC_API_KEY", "MY_CUSTOM_SECRET", "UNSET_VAR"}, func(k string) string {
		return values[k]
	})
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]string{}
	for _, e := range env {
		byName[e.Name] = e.Value
	}

	sealed, ok := byName["ANTHROPIC_AUTH_TOKEN"]
	if !ok || !strings.HasPrefix(sealed, "brk1_") {
		t.Fatalf("expected sealed blob in ANTHROPIC_AUTH_TOKEN, got %q", sealed)
	}
	if _, present := byName["ANTHROPIC_API_KEY"]; present {
		t.Fatal("raw ANTHROPIC_API_KEY must not be present in injector mode")
	}
	if byName["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:3128/t/api.anthropic.com" {
		t.Fatalf("unexpected base url: %q", byName["ANTHROPIC_BASE_URL"])
	}
	if byName["MY_CUSTOM_SECRET"] != "raw-value" {
		t.Fatal("unknown credentials fall back to raw injection")
	}
	if _, present := byName["UNSET_VAR"]; present {
		t.Fatal("unset credentials must be skipped")
	}

	secret, err := injector.Open(pub, priv, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if secret.Token != "sk-ant-real" || !secret.HostAllowed("api.anthropic.com") || secret.HostAllowed("evil.com") {
		t.Fatalf("sealed secret round trip wrong: %+v", secret)
	}
}
