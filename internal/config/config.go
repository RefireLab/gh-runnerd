package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/RefireLab/gh-runnerd/internal/layout"
	"github.com/RefireLab/gh-runnerd/internal/netbridge"
	"github.com/RefireLab/gh-runnerd/internal/runnerimages"
	"github.com/RefireLab/gh-runnerd/internal/units"
	"github.com/pelletier/go-toml/v2"
)

// Config is the full daemon and CLI configuration.
type Config struct {
	DataDir  string         `toml:"data_dir"`
	GitHub   GitHubConfig   `toml:"github"`
	Runner   RunnerConfig   `toml:"runner"`
	Pool     PoolConfig     `toml:"pool"`
	VM       VMConfig       `toml:"vm"`
	Image    ImageConfig    `toml:"image"`
	Registry RegistryConfig `toml:"registry"`
	Network  NetworkConfig  `toml:"network"`
	Webhook  WebhookConfig  `toml:"webhook"`
}

type GitHubConfig struct {
	BaseURL           string   `toml:"base_url"`
	Token             string   `toml:"token"`
	AppID             int64    `toml:"app_id"`
	AppPrivateKeyPath string   `toml:"app_private_key_path"`
	InstallationID    int64    `toml:"installation_id"`
	Scope             string   `toml:"scope"` // repo | org
	Owner             string   `toml:"owner"`
	Repo              string   `toml:"repo"`
	Org               string   `toml:"org"`
	RunnerGroupID     int64    `toml:"runner_group_id"`
	PollInterval      duration `toml:"poll_interval"`
	PollRepos         []string `toml:"poll_repos"`
	DockerHubUsername string   `toml:"dockerhub_username"`
	DockerHubToken    string   `toml:"dockerhub_token"`
}

type RunnerConfig struct {
	Labels     []string `toml:"labels"`
	NamePrefix string   `toml:"name_prefix"`
	// CleanupInterval is how often the daemon sweeps stale runner
	// registrations (Offline leftovers of crashed VMs or a killed daemon)
	// out of GitHub. Non-positive means the default.
	CleanupInterval duration `toml:"cleanup_interval"`
}

type PoolConfig struct {
	MinIdle          int      `toml:"min_idle"`
	MaxConcurrent    int      `toml:"max_concurrent"`
	JobTimeout       duration `toml:"job_timeout"`
	RecycleIdleAfter duration `toml:"recycle_idle_after"`
}

type VMConfig struct {
	CPUs        int      `toml:"cpus"`
	Memory      string   `toml:"memory"`
	Disk        string   `toml:"disk"`
	Template    string   `toml:"template"`
	BootTimeout duration `toml:"boot_timeout"`
}

// ImageConfig selects what the baked runner image contains.
//
//	flavor   = "minimal" | "essential" | "full"
//	upstream = actions/runner-images image name, e.g. "ubuntu-24.04"
//	upstream_ref = optional pin: a release tag like "ubuntu24/20260816.277"
//	               or a branch; empty means the newest release.
type ImageConfig struct {
	Flavor      string `toml:"flavor"`
	Upstream    string `toml:"upstream"`
	UpstreamRef string `toml:"upstream_ref"`
}

type RegistryConfig struct {
	Listen            string `toml:"listen"`
	PinnedQuota       string `toml:"pinned_quota"`
	CacheQuota        string `toml:"cache_quota"`
	DockerHubUsername string `toml:"dockerhub_username"`
	DockerHubToken    string `toml:"dockerhub_token"`
}

type NetworkConfig struct {
	Bridge       string `toml:"bridge"`
	CIDR         string `toml:"cidr"`
	HostIP       string `toml:"host_ip"`
	GuestPort    int    `toml:"guest_port"`
	RegistryPort int    `toml:"registry_port"`
}

type WebhookConfig struct {
	Listen string `toml:"listen"`
	Path   string `toml:"path"`
	Secret string `toml:"secret"`
}

type duration struct {
	time.Duration
}

func (d duration) MarshalText() ([]byte, error) {
	return []byte(d.Duration.String()), nil
}

func (d *duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// Defaults returns a complete config with first-run values.
func Defaults() Config {
	return Config{
		DataDir: defaultDataDir(),
		GitHub: GitHubConfig{
			BaseURL:       "https://api.github.com",
			Scope:         "repo",
			RunnerGroupID: 1,
			PollInterval:  duration{15 * time.Second},
		},
		Runner: RunnerConfig{
			Labels:          []string{"gh-runnerd"},
			NamePrefix:      "gh-runnerd",
			CleanupInterval: duration{3 * time.Minute},
		},
		Pool: PoolConfig{
			MinIdle:          0,
			MaxConcurrent:    4,
			JobTimeout:       duration{6 * time.Hour},
			RecycleIdleAfter: duration{45 * time.Minute},
		},
		VM: VMConfig{
			CPUs:        2,
			Memory:      "4G",
			Disk:        "40G",
			Template:    "ubuntu-24.04",
			BootTimeout: duration{90 * time.Second},
		},
		Image: ImageConfig{
			Flavor:   string(runnerimages.FlavorMinimal),
			Upstream: "ubuntu-24.04",
		},
		Registry: RegistryConfig{
			// Listen is derived from network.host_ip and the default local
			// port when empty; see RegistryListen.
			Listen:      "",
			PinnedQuota: "50G",
			CacheQuota:  "50G",
		},
		Network: NetworkConfig{
			Bridge:       "br-ghrunnerd",
			CIDR:         "10.87.0.0/16",
			HostIP:       "10.87.0.1",
			GuestPort:    5099,
			RegistryPort: 443,
		},
		Webhook: WebhookConfig{
			Listen: "127.0.0.1:8080",
			Path:   "/webhook",
		},
	}
}

func defaultDataDir() string {
	if v := os.Getenv("GH_RUNNERD_DATA_DIR"); v != "" {
		return v
	}
	if os.Geteuid() == 0 {
		return "/var/lib/gh-runnerd"
	}
	return "./gh-runnerd-data"
}

// Load reads TOML from path and fills defaults for omitted fields.
func Load(path string) (Config, error) {
	cfg := Defaults()
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.DataDir == "" {
		cfg.DataDir = defaultDataDir()
	}
	// A relative data_dir lives next to the config file so a portable
	// gh-runnerd folder keeps working when moved or run from elsewhere.
	if !filepath.IsAbs(cfg.DataDir) {
		if abs, absErr := filepath.Abs(path); absErr == nil {
			cfg.DataDir = filepath.Join(filepath.Dir(abs), cfg.DataDir)
		}
	}
	cfg.applyEnv()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("GH_RUNNERD_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("GH_RUNNERD_GITHUB_TOKEN"); v != "" {
		c.GitHub.Token = v
	}
	if v := os.Getenv("GH_RUNNERD_WEBHOOK_SECRET"); v != "" {
		c.Webhook.Secret = v
	}
}

// Validate checks semantically required fields that are always needed.
func (c Config) Validate() error {
	if err := c.validateCleanStrings(); err != nil {
		return err
	}
	if c.Pool.MaxConcurrent < 1 {
		return fmt.Errorf("pool.max_concurrent must be >= 1")
	}
	if c.Pool.MinIdle < 0 || c.Pool.MinIdle > c.Pool.MaxConcurrent {
		return fmt.Errorf("pool.min_idle must be between 0 and pool.max_concurrent")
	}
	if c.VM.CPUs < 1 {
		return fmt.Errorf("vm.cpus must be >= 1")
	}
	if _, err := units.ParseMB(c.VM.Memory); err != nil {
		return fmt.Errorf("vm.memory: %w", err)
	}
	if _, err := units.ParseGB(c.VM.Disk); err != nil {
		return fmt.Errorf("vm.disk: %w", err)
	}
	if _, err := units.ParseBytes(c.Registry.PinnedQuota); err != nil {
		return fmt.Errorf("registry.pinned_quota: %w", err)
	}
	if _, err := units.ParseBytes(c.Registry.CacheQuota); err != nil {
		return fmt.Errorf("registry.cache_quota: %w", err)
	}
	if len(c.Runner.Labels) == 0 {
		return fmt.Errorf("runner.labels must not be empty")
	}
	scope := strings.ToLower(c.GitHub.Scope)
	if scope != "repo" && scope != "org" {
		return fmt.Errorf("github.scope must be repo or org")
	}
	if _, err := runnerimages.ParseFlavor(c.Image.Flavor); err != nil {
		return fmt.Errorf("image.flavor: %w", err)
	}
	if c.Image.Upstream != "" && !runnerimages.ValidFamily(c.Image.Upstream) {
		return fmt.Errorf("image.upstream %q is not an upstream Ubuntu image name (e.g. ubuntu-24.04)", c.Image.Upstream)
	}
	hostIP := net.ParseIP(c.Network.HostIP)
	if hostIP == nil || hostIP.To4() == nil {
		return fmt.Errorf("network.host_ip %q is not an IPv4 address", c.Network.HostIP)
	}
	_, ipnet, err := net.ParseCIDR(c.Network.CIDR)
	if err != nil {
		return fmt.Errorf("network.cidr: %w", err)
	}
	if !ipnet.Contains(hostIP) {
		return fmt.Errorf("network.host_ip %s is outside network.cidr %s", c.Network.HostIP, c.Network.CIDR)
	}
	if c.VM.Template != "ubuntu-24.04" && !strings.HasPrefix(c.VM.Template, "ubuntu-24.04") {
		// Custom names are allowed once imported; the shipped template is ubuntu-24.04.
	}
	return nil
}

// validateCleanStrings rejects control characters in identity fields.
// Terminal escape codes (arrow keys pressed in an old init wizard) used
// to be recorded verbatim and then broke every GitHub URL built from the
// value, failing at runtime instead of at load.
func (c Config) validateCleanStrings() error {
	fields := []struct{ name, value string }{
		{"github.base_url", c.GitHub.BaseURL},
		{"github.owner", c.GitHub.Owner},
		{"github.repo", c.GitHub.Repo},
		{"github.org", c.GitHub.Org},
		{"runner.name_prefix", c.Runner.NamePrefix},
		{"vm.template", c.VM.Template},
		{"image.flavor", c.Image.Flavor},
		{"image.upstream", c.Image.Upstream},
		{"image.upstream_ref", c.Image.UpstreamRef},
	}
	for i, r := range c.GitHub.PollRepos {
		fields = append(fields, struct{ name, value string }{fmt.Sprintf("github.poll_repos[%d]", i), r})
	}
	for i, l := range c.Runner.Labels {
		fields = append(fields, struct{ name, value string }{fmt.Sprintf("runner.labels[%d]", i), l})
	}
	for _, f := range fields {
		if hasControlChars(f.value) {
			return fmt.Errorf("%s contains control characters (arrow-key escape codes recorded by an older init wizard) — re-run sudo gh-runnerd init or fix the value: %q", f.name, f.value)
		}
	}
	return nil
}

func hasControlChars(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// Layout returns the data-directory map for this config.
func (c Config) Layout() layout.Dirs {
	return layout.New(c.DataDir)
}

// CleanupInterval returns runner.cleanup_interval, defaulting to three
// minutes when unset or non-positive.
func (c Config) CleanupInterval() time.Duration {
	if c.Runner.CleanupInterval.Duration > 0 {
		return c.Runner.CleanupInterval.Duration
	}
	return 3 * time.Minute
}

// MemoryMB returns vm.memory in mebibytes.
func (c Config) MemoryMB() int {
	n, _ := units.ParseMB(c.VM.Memory)
	return n
}

// DiskGB returns vm.disk in gibibytes.
func (c Config) DiskGB() int {
	n, _ := units.ParseGB(c.VM.Disk)
	return n
}

// RegistryListen returns the embedded registry bind address. When
// registry.listen is empty it derives <network.host_ip>:42443 — a high
// port so the host's real 443 (web servers, docker-proxy) is never
// claimed; VMs still dial 443 via an iptables redirect on the bridge.
func (c Config) RegistryListen() string {
	if strings.TrimSpace(c.Registry.Listen) != "" {
		return c.Registry.Listen
	}
	return net.JoinHostPort(c.Network.HostIP, strconv.Itoa(netbridge.DefaultRegistryLocalPort))
}

// RegistryLocalPort returns the TCP port of RegistryListen.
func (c Config) RegistryLocalPort() int {
	_, portStr, err := net.SplitHostPort(c.RegistryListen())
	if err != nil {
		return netbridge.DefaultRegistryLocalPort
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return netbridge.DefaultRegistryLocalPort
	}
	return p
}

// PinnedQuotaBytes returns the pinned image quota.
func (c Config) PinnedQuotaBytes() int64 {
	n, _ := units.ParseBytes(c.Registry.PinnedQuota)
	return n
}

// CacheQuotaBytes returns the pull-through cache quota.
func (c Config) CacheQuotaBytes() int64 {
	n, _ := units.ParseBytes(c.Registry.CacheQuota)
	return n
}

// WriteFile writes cfg as TOML.
func WriteFile(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// Find looks for a config file from flags, CWD, and /etc.
func Find(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	candidates := []string{
		"gh-runnerd.toml",
		filepath.Join(".", "gh-runnerd.toml"),
		"/etc/gh-runnerd/config.toml",
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "gh-runnerd", "config.toml"))
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("no config file found (tried gh-runnerd.toml and /etc/gh-runnerd/config.toml); run gh-runnerd init")
}

// HasGitHubAuth reports whether a PAT or GitHub App is configured.
func (c Config) HasGitHubAuth() bool {
	if strings.TrimSpace(c.GitHub.Token) != "" {
		return true
	}
	return c.GitHub.AppID != 0 && c.GitHub.InstallationID != 0 && c.GitHub.AppPrivateKeyPath != ""
}
