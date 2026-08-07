package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveSettingsDoesNotDeleteUnexpectedDirectory(t *testing.T) {
	dataDir := t.TempDir()
	settingsPath := filepath.Join(dataDir, "settings.json")
	if err := os.Mkdir(settingsPath, 0o700); err != nil {
		t.Fatal(err)
	}

	err := SaveSettings(dataDir, Settings{LLMProvider: "anthropic"})
	if err == nil {
		t.Fatal("SaveSettings replaced an existing settings.json directory; want a safe failure")
	}

	info, statErr := os.Stat(settingsPath)
	if statErr != nil {
		t.Fatalf("settings.json path was removed after failed save: %v", statErr)
	}
	if !info.IsDir() {
		t.Fatal("settings.json directory was destructively replaced by a regular file")
	}
}

func TestSaveSettingsReplacesExistingRegularFile(t *testing.T) {
	dataDir := t.TempDir()
	settingsPath := filepath.Join(dataDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"llm_provider":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	want := Settings{LLMProvider: "openai", QueryConcurrency: 7}
	if err := SaveSettings(dataDir, want); err != nil {
		t.Fatalf("SaveSettings existing file: %v", err)
	}
	got := LoadSettings(dataDir)
	if got.LLMProvider != want.LLMProvider || got.QueryConcurrency != want.QueryConcurrency {
		t.Fatalf("saved settings = %+v, want provider=%q query_concurrency=%d", got, want.LLMProvider, want.QueryConcurrency)
	}

	info, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("settings.json mode = %o, want 600", info.Mode().Perm())
	}
}
