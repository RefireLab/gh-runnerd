package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsValidate(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Runner.Labels[0] != "gh-runnerd" {
		t.Fatalf("default label: %v", cfg.Runner.Labels)
	}
}

func TestLoadRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := Defaults()
	cfg.DataDir = dir
	cfg.GitHub.Token = "ghs_test"
	cfg.GitHub.Owner = "acme"
	cfg.GitHub.Repo = "app"
	if err := WriteFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.GitHub.Owner != "acme" || got.GitHub.Repo != "app" {
		t.Fatalf("unexpected github scope: %+v", got.GitHub)
	}
	if got.Pool.MaxConcurrent != 4 {
		t.Fatalf("max concurrent %d", got.Pool.MaxConcurrent)
	}
}

func TestValidateRejectsBadPool(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	cfg.Pool.MinIdle = 8
	cfg.Pool.MaxConcurrent = 2
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error")
	}
}

func TestGitHubTokenFromEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := Defaults()
	cfg.DataDir = dir
	if err := WriteFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_RUNNERD_GITHUB_TOKEN", "ghs_from_env")
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.GitHub.Token != "ghs_from_env" {
		t.Fatalf("token %q", got.GitHub.Token)
	}
	_ = os.Getenv
}
