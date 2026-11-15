// Package config loads environment configuration and the model catalog.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	WorkerImage           = "ghcr.io/kithlo-server/coder-worker:latest"
	ShellImage            = "ghcr.io/kithlo-server/coder-shell:latest"
	MaxMessageLength      = 4000
	WorkspaceDir          = "/workspace"
	UploadDir             = "/upload"
	MountDir              = "/opt"
	MaxUploadBytes        = 50 * 1024 * 1024
	StateVersion          = 4
	NoActiveRepoMessage   = "No active repository. Use `/create` or `/set` first."
	GoogleCredentialsPath = "/root/.gcp-credentials.json"
)

// ModelEntry is one model in the OPENCODE_MODELS catalog.
type ModelEntry struct {
	ID       string
	Name     string
	Variants []string
	Group    string
	Provider string
}

// ProviderGroup keeps the providers of one catalog name in JSON order.
type ProviderGroup struct {
	Names  []string
	ByName map[string]*ModelGroup
}

// ModelGroup keeps the models of one provider in JSON order.
type ModelGroup struct {
	IDs  []string
	ByID map[string]*ModelEntry
}

// Catalog is the ordered OPENCODE_MODELS catalog.
type Catalog struct {
	Names          []string
	ByName         map[string]*ProviderGroup
	ByID           map[string]*ModelEntry
	DefaultID      string
	DefaultVariant *string
}

// Entry returns the catalog entry of a model id, if configured.
func (c *Catalog) Entry(id string) *ModelEntry {
	if c == nil {
		return nil
	}
	return c.ByID[id]
}

// Label returns the display name of a model, falling back to its id.
func (c *Catalog) Label(id string) string {
	if entry := c.Entry(id); entry != nil {
		return entry.Name
	}
	return id
}

// Variants returns the configured variants of a model.
func (c *Catalog) Variants(id string) []string {
	if entry := c.Entry(id); entry != nil {
		return append([]string(nil), entry.Variants...)
	}
	return nil
}

// Config is the immutable process configuration.
type Config struct {
	TelegramToken    string
	TelegramAPIBase  string
	TelegramFileBase string
	DeepseekAPIKey   string
	OpenRouterKey    string

	VertexProject     string
	VertexLocation    string
	CredentialsJSON   string
	ReposDir          string
	UploadsDir        string
	SSHKey            string
	AllowedUserIDs    map[string]bool
	GitUserName       string
	GitUserEmail      string
	DataDir           string
	StateFile         string
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	ConnectTimeout    time.Duration
	PoolTimeout       time.Duration
	MediaWriteTimeout time.Duration

	Catalog *Catalog
}

// Load reads all configuration from the process environment.
func Load() (*Config, error) {
	return LoadFromEnv(os.Getenv)
}

// LoadFromEnv reads configuration through getenv so tests can supply values.
func LoadFromEnv(getenv func(string) string) (*Config, error) {
	catalog, err := LoadCatalog(getenv("OPENCODE_MODELS"))
	if err != nil {
		return nil, err
	}

	allowed := make(map[string]bool)
	for _, id := range strings.Split(getenv("ALLOWED_USER_ID"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			allowed[id] = true
		}
	}

	vertexProject := getenv("GOOGLE_VERTEX_PROJECT")
	if vertexProject == "" {
		vertexProject = getenv("GOOGLE_CLOUD_PROJECT")
	}
	vertexLocation := getenv("GOOGLE_VERTEX_LOCATION")
	if vertexLocation == "" {
		vertexLocation = "global"
	}
	dataDir := getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "/app/data"
	}

	return &Config{
		TelegramToken:     getenv("TELEGRAM_BOT_TOKEN"),
		TelegramAPIBase:   getenv("TELEGRAM_API_BASE_URL"),
		TelegramFileBase:  getenv("TELEGRAM_API_FILE_BASE_URL"),
		DeepseekAPIKey:    getenv("DEEPSEEK_API_KEY"),
		OpenRouterKey:     getenv("OPENROUTER_API_KEY"),
		VertexProject:     vertexProject,
		VertexLocation:    vertexLocation,
		CredentialsJSON:   getenv("GOOGLE_APPLICATION_CREDENTIALS_JSON"),
		ReposDir:          envOr(getenv, "REPOS_DIR", "/repos"),
		UploadsDir:        envOr(getenv, "UPLOADS_DIR", UploadDir),
		SSHKey:            getenv("OPENCODE_SSH_KEY"),
		AllowedUserIDs:    allowed,
		GitUserName:       getenv("GIT_USER_NAME"),
		GitUserEmail:      getenv("GIT_USER_EMAIL"),
		DataDir:           dataDir,
		StateFile:         dataDir + "/repos_state.json",
		ReadTimeout:       seconds(getenv, "TELEGRAM_READ_TIMEOUT", 60),
		WriteTimeout:      seconds(getenv, "TELEGRAM_WRITE_TIMEOUT", 300),
		ConnectTimeout:    seconds(getenv, "TELEGRAM_CONNECT_TIMEOUT", 30),
		PoolTimeout:       seconds(getenv, "TELEGRAM_POOL_TIMEOUT", 30),
		MediaWriteTimeout: seconds(getenv, "TELEGRAM_MEDIA_WRITE_TIMEOUT", 300),
		Catalog:           catalog,
	}, nil
}

func envOr(getenv func(string) string, name, fallback string) string {
	if value := getenv(name); value != "" {
		return value
	}
	return fallback
}

func seconds(getenv func(string) string, name string, fallback float64) time.Duration {
	value := getenv(name)
	if value == "" {
		return time.Duration(fallback * float64(time.Second))
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return time.Duration(fallback * float64(time.Second))
	}
	return time.Duration(parsed * float64(time.Second))
}

// LoadCatalog parses and validates OPENCODE_MODELS, preserving JSON order.
func LoadCatalog(raw string) (*Catalog, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("OPENCODE_MODELS environment variable is missing")
	}

	rootKeys, root, err := decodeObject([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("OPENCODE_MODELS is not valid JSON: %w", err)
	}
	_ = rootKeys

	if version, ok := root["version"]; !ok || string(version) == "null" {
		return nil, fmt.Errorf("OPENCODE_MODELS must have version 1")
	} else {
		var v int
		if err := json.Unmarshal(version, &v); err != nil || v != 1 {
			return nil, fmt.Errorf("OPENCODE_MODELS must have version 1")
		}
	}

	modelsRaw, ok := root["models"]
	if !ok {
		return nil, fmt.Errorf("OPENCODE_MODELS must contain a non-empty 'models' object")
	}
	nameKeys, names, err := decodeObject(modelsRaw)
	if err != nil || len(names) == 0 {
		return nil, fmt.Errorf("OPENCODE_MODELS must contain a non-empty 'models' object")
	}

	catalog := &Catalog{
		ByName: make(map[string]*ProviderGroup),
		ByID:   make(map[string]*ModelEntry),
	}
	for _, name := range nameKeys {
		providerKeys, providers, err := decodeObject(names[name])
		if err != nil {
			return nil, fmt.Errorf("OPENCODE_MODELS: '%s' must be an object of providers", name)
		}
		group := &ProviderGroup{ByName: make(map[string]*ModelGroup)}
		for _, provider := range providerKeys {
			group.Names = append(group.Names, provider)
			modelKeys, models, err := decodeObject(providers[provider])
			if err != nil {
				return nil, fmt.Errorf("OPENCODE_MODELS: '%s' -> '%s' must be an object of models", name, provider)
			}
			modelsGroup := &ModelGroup{ByID: make(map[string]*ModelEntry)}
			for _, modelID := range modelKeys {
				infoKeys, info, err := decodeObject(models[modelID])
				if err != nil {
					return nil, fmt.Errorf("OPENCODE_MODELS: model '%s' must be an object", modelID)
				}
				_ = infoKeys
				entry := &ModelEntry{
					ID:       modelID,
					Name:     modelID,
					Group:    name,
					Provider: provider,
					Variants: []string{},
				}
				if rawName, ok := info["name"]; ok && string(rawName) != "null" {
					var display any
					if err := json.Unmarshal(rawName, &display); err == nil {
						if text, ok := display.(string); ok && text != "" {
							entry.Name = text
						}
					}
				}
				if rawVariants, ok := info["variants"]; ok && string(rawVariants) != "null" {
					var variants []any
					if err := json.Unmarshal(rawVariants, &variants); err != nil {
						return nil, fmt.Errorf("OPENCODE_MODELS: model '%s' variants must be a list of strings", modelID)
					}
					for _, variant := range variants {
						text, ok := variant.(string)
						if !ok || text == "" {
							return nil, fmt.Errorf("OPENCODE_MODELS: model '%s' variants must be a list of strings", modelID)
						}
						entry.Variants = append(entry.Variants, text)
					}
				}
				modelsGroup.IDs = append(modelsGroup.IDs, modelID)
				modelsGroup.ByID[modelID] = entry
				catalog.ByID[modelID] = entry
			}
			group.ByName[provider] = modelsGroup
		}
		catalog.Names = append(catalog.Names, name)
		catalog.ByName[name] = group
	}

	defaultRaw, ok := root["default_model"]
	if !ok {
		return nil, fmt.Errorf("OPENCODE_MODELS must contain a 'default_model' object")
	}
	_, defaultModel, err := decodeObject(defaultRaw)
	if err != nil {
		return nil, fmt.Errorf("OPENCODE_MODELS must contain a 'default_model' object")
	}
	idRaw, ok := defaultModel["id"]
	if !ok {
		return nil, fmt.Errorf("OPENCODE_MODELS default_model.id must reference a listed model")
	}
	var defaultID string
	if err := json.Unmarshal(idRaw, &defaultID); err != nil {
		return nil, fmt.Errorf("OPENCODE_MODELS default_model.id must reference a listed model")
	}
	if _, ok := catalog.ByID[defaultID]; !ok {
		return nil, fmt.Errorf("OPENCODE_MODELS default_model.id must reference a listed model")
	}
	catalog.DefaultID = defaultID

	if variantRaw, ok := defaultModel["variant"]; ok && string(variantRaw) != "null" {
		var variant string
		if err := json.Unmarshal(variantRaw, &variant); err != nil {
			return nil, fmt.Errorf("OPENCODE_MODELS default_model.variant must be a string")
		}
		if variant != "" {
			catalog.DefaultVariant = &variant
		}
	}

	return catalog, nil
}

// decodeObject decodes a JSON object into its ordered keys and raw values.
func decodeObject(raw []byte) ([]string, map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, nil, fmt.Errorf("expected object")
	}
	var keys []string
	values := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, nil, fmt.Errorf("expected string key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, nil, err
		}
		keys = append(keys, key)
		values[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, nil, err
	}
	return keys, values, nil
}

// DecodeObject exposes ordered object decoding to the state package.
func DecodeObject(raw []byte) ([]string, map[string]json.RawMessage, error) {
	return decodeObject(raw)
}
