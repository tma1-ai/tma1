package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Settings holds user-configurable settings persisted to ~/.tma1/settings.json.
// Env vars take priority over file settings.
//
// New fields must default safely when missing — older settings.json files will
// unmarshal them as zero values, and ApplySettings must skip those.
type Settings struct {
	LLMAPIKey        string `json:"llm_api_key"`
	LLMProvider      string `json:"llm_provider"`
	LLMModel         string `json:"llm_model"`
	LogLevel         string `json:"log_level"`
	DataTTL          string `json:"data_ttl"`
	QueryConcurrency int    `json:"query_concurrency,omitempty"`
}

// LoadSettings reads settings from dataDir/settings.json.
// Returns zero-value Settings if the file doesn't exist or is unreadable.
func LoadSettings(dataDir string) Settings {
	path := filepath.Join(dataDir, "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return Settings{}
	}
	var s Settings
	_ = json.Unmarshal(data, &s)
	return s
}

// SaveSettings writes settings through a same-directory temp file and replaces
// settings.json with os.Rename. On Unix this is an atomic file replacement; on
// non-Unix platforms Go does not promise atomicity, but Rename still replaces
// an existing non-directory destination. Never pre-delete the target: doing so
// creates a data-loss window and can destructively remove an unexpected empty
// directory at the settings path.
func SaveSettings(dataDir string, s Settings) error {
	path := filepath.Join(dataDir, "settings.json")
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// EnvOverrides returns the list of setting keys that are locked by environment variables.
func EnvOverrides() []string {
	var overrides []string
	if os.Getenv("TMA1_LLM_API_KEY") != "" {
		overrides = append(overrides, "llm_api_key")
	}
	if os.Getenv("TMA1_LLM_PROVIDER") != "" {
		overrides = append(overrides, "llm_provider")
	}
	if os.Getenv("TMA1_LLM_MODEL") != "" {
		overrides = append(overrides, "llm_model")
	}
	if os.Getenv("TMA1_LOG_LEVEL") != "" {
		overrides = append(overrides, "log_level")
	}
	if os.Getenv("TMA1_DATA_TTL") != "" {
		overrides = append(overrides, "data_ttl")
	}
	if os.Getenv("TMA1_QUERY_CONCURRENCY") != "" {
		overrides = append(overrides, "query_concurrency")
	}
	return overrides
}

// ApplySettings merges file settings into a Config, respecting env var priority.
// Env vars always win; file settings fill in the gaps.
func ApplySettings(cfg *Config, s Settings) {
	if os.Getenv("TMA1_LLM_API_KEY") == "" && s.LLMAPIKey != "" {
		cfg.LLMAPIKey = s.LLMAPIKey
	}
	if os.Getenv("TMA1_LLM_PROVIDER") == "" && s.LLMProvider != "" {
		cfg.LLMProvider = s.LLMProvider
	}
	if os.Getenv("TMA1_LLM_MODEL") == "" && s.LLMModel != "" {
		cfg.LLMModel = s.LLMModel
	}
	if os.Getenv("TMA1_LOG_LEVEL") == "" && s.LogLevel != "" {
		cfg.LogLevel = s.LogLevel
	}
	if os.Getenv("TMA1_DATA_TTL") == "" && s.DataTTL != "" {
		cfg.DataTTL = s.DataTTL
	}
	// Only apply if persisted explicitly. Older settings.json files have 0 here;
	// the env-default from config.Load already populated cfg.QueryConcurrency.
	if os.Getenv("TMA1_QUERY_CONCURRENCY") == "" && s.QueryConcurrency > 0 {
		cfg.QueryConcurrency = s.QueryConcurrency
	}
}
