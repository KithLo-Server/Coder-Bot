package config

import (
	"strings"
	"testing"
)

const testModels = `{
  "version": 1,
  "models": {
    "DeepSeek": {
      "OpenRouter": {
        "deepseek/deepseek-flash": {"name": "DeepSeek Flash", "variants": ["low", "high", "max"]}
      },
      "OpenCode": {
        "opencode/free": {"name": "Free", "variants": []}
      }
    },
    "Anthropic": {
      "Google Vertex": {
        "google-vertex/claude": {"name": "Claude", "variants": ["low", "high"]}
      }
    },
    "Local": {"Ollama": {}}
  },
  "default_model": {"id": "deepseek/deepseek-flash", "variant": "low"}
}`

func TestLoadCatalogPreservesOrder(t *testing.T) {
	catalog, err := LoadCatalog(testModels)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(catalog.Names, ","); got != "DeepSeek,Anthropic,Local" {
		t.Fatalf("names = %s", got)
	}
	if got := strings.Join(catalog.ByName["DeepSeek"].Names, ","); got != "OpenRouter,OpenCode" {
		t.Fatalf("providers = %s", got)
	}
	entry := catalog.Entry("deepseek/deepseek-flash")
	if entry == nil || entry.Name != "DeepSeek Flash" || entry.Group != "DeepSeek" || entry.Provider != "OpenRouter" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if catalog.DefaultID != "deepseek/deepseek-flash" || catalog.DefaultVariant == nil || *catalog.DefaultVariant != "low" {
		t.Fatalf("unexpected default: %s %v", catalog.DefaultID, catalog.DefaultVariant)
	}
}

func TestLoadCatalogValidation(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"missing", "", "missing"},
		{"bad json", "{", "not valid JSON"},
		{"bad version", `{"version":2,"models":{"a":{"b":{"c":{}}}},"default_model":{"id":"c"}}`, "version 1"},
		{"no models", `{"version":1,"models":{},"default_model":{"id":"c"}}`, "non-empty 'models'"},
		{"bad default", `{"version":1,"models":{"a":{"b":{"c":{}}}},"default_model":{"id":"x"}}`, "default_model.id"},
		{"bad variants", `{"version":1,"models":{"a":{"b":{"c":{"variants":[1]}}}},"default_model":{"id":"c"}}`, "list of strings"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadCatalog(tc.raw)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestLoadFromEnvDefaults(t *testing.T) {
	values := map[string]string{
		"OPENCODE_MODELS": testModels,
		"DATA_DIR":        "/tmp/data",
	}
	cfg, err := LoadFromEnv(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReposDir != "/repos" || cfg.UploadsDir != "/upload" || cfg.StateFile != "/tmp/data/repos_state.json" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.ReadTimeout.Seconds() != 60 || cfg.MediaWriteTimeout.Seconds() != 300 || cfg.VertexLocation != "global" {
		t.Fatalf("unexpected timeouts: %+v", cfg)
	}
}
