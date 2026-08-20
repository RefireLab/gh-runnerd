package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestValidateImageSection(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	if cfg.Image.Flavor != "minimal" || cfg.Image.Upstream != "ubuntu-24.04" {
		t.Fatalf("image defaults: %+v", cfg.Image)
	}
	cfg.Image.Flavor = "essential"
	cfg.Image.Upstream = "ubuntu-26.04"
	cfg.Image.UpstreamRef = "ubuntu26/20260817.108"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Image.Flavor = "kitchen-sink"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "image.flavor") {
		t.Fatalf("bad flavor accepted: %v", err)
	}
	cfg.Image.Flavor = "full"
	cfg.Image.Upstream = "debian-12"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "image.upstream") {
		t.Fatalf("bad upstream accepted: %v", err)
	}
}

func TestValidateRejectsControlCharacters(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	cfg.GitHub.PollRepos = []string{"RefireLab/pitstop-ae\x1b[D\x1b[D"}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("escape codes in poll_repos must be rejected")
	}
	if !strings.Contains(err.Error(), "github.poll_repos[0]") {
		t.Fatalf("error must name the field: %v", err)
	}
	cfg = Defaults()
	cfg.Runner.Labels = []string{"gh-runnerd", "bad\x1blabel"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("escape codes in labels must be rejected")
	}
	cfg = Defaults()
	cfg.GitHub.Org = "Refire\x7fLab"
	if err := cfg.Validate(); err == nil {
		t.Fatal("control characters in org must be rejected")
	}
}

func TestRegistryListenDerived(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	if got := cfg.RegistryListen(); got != "10.87.0.1:42443" {
		t.Fatalf("derived listen %q", got)
	}
	if cfg.RegistryLocalPort() != 42443 {
		t.Fatalf("port %d", cfg.RegistryLocalPort())
	}
	cfg.Network.HostIP = "10.99.0.1"
	if got := cfg.RegistryListen(); got != "10.99.0.1:42443" {
		t.Fatalf("derived listen %q", got)
	}
	cfg.Registry.Listen = "10.99.0.1:443"
	if got := cfg.RegistryListen(); got != "10.99.0.1:443" || cfg.RegistryLocalPort() != 443 {
		t.Fatalf("explicit listen %q port %d", got, cfg.RegistryLocalPort())
	}
}

func TestValidateRejectsBadNetwork(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	cfg.Network.HostIP = "not-an-ip"
	if err := cfg.Validate(); err == nil {
		t.Fatal("bad host_ip must fail")
	}
	cfg = Defaults()
	cfg.Network.HostIP = "192.168.1.1"
	if err := cfg.Validate(); err == nil {
		t.Fatal("host_ip outside cidr must fail")
	}
	cfg = Defaults()
	cfg.Network.HostIP = "10.99.0.1"
	cfg.Network.CIDR = "10.99.0.0/16"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("custom consistent network: %v", err)
	}
}

func TestRelativeDataDirResolvesToConfigDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh-runnerd.toml")
	cfg := Defaults()
	cfg.DataDir = "gh-runnerd-data"
	if err := WriteFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "gh-runnerd-data")
	if got.DataDir != want {
		t.Fatalf("data dir %q, want %q", got.DataDir, want)
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
