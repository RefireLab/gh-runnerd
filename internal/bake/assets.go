package bake

import "embed"

// assetsFS holds the files installed into the golden VM: the guest agent
// systemd unit, /etc/hosts entries for the local registry, and Docker
// registry mirror configuration.
//
//go:embed assets
var assetsFS embed.FS

// seedAssets maps embedded asset names to their file name inside the seed
// share mounted by the bake VM.
var seedAssets = map[string]string{
	"assets/gh-runnerd-guest.service": "gh-runnerd-guest.service",
	"assets/hosts":                    "hosts",
	"assets/docker-daemon.json":       "daemon.json",
	"assets/hosts.toml.docker":        "hosts.toml.docker",
	"assets/hosts.toml.ghcr":          "hosts.toml.ghcr",
	"assets/hosts.toml.quay":          "hosts.toml.quay",
}
