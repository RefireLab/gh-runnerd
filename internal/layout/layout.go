package layout

import (
	"fmt"
	"os"
	"path/filepath"
)

// Dirs is the on-disk layout for one gh-runnerd installation.
type Dirs struct {
	Root       string
	Images     string
	Runner     string
	Containers string
	Imports    string
	Jobs       string
	Runtime    string
	Cache      string
	CacheOCI   string
	State      string
	CA         string
}

// New returns paths under root without creating them.
func New(root string) Dirs {
	root = filepath.Clean(root)
	images := filepath.Join(root, "images")
	cache := filepath.Join(root, "cache")
	state := filepath.Join(root, "state")
	return Dirs{
		Root:       root,
		Images:     images,
		Runner:     filepath.Join(images, "runner"),
		Containers: filepath.Join(images, "containers"),
		Imports:    filepath.Join(root, "imports"),
		Jobs:       filepath.Join(root, "jobs"),
		Runtime:    filepath.Join(root, "runtime"),
		Cache:      cache,
		CacheOCI:   filepath.Join(cache, "oci"),
		State:      state,
		CA:         filepath.Join(state, "ca"),
	}
}

// Ensure creates every managed directory with 0700 permissions.
func (d Dirs) Ensure() error {
	for _, dir := range []string{
		d.Root, d.Images, d.Runner, d.Containers, d.Imports,
		d.Jobs, d.Runtime, d.Cache, d.CacheOCI, d.State, d.CA,
		filepath.Join(d.Containers, "blobs", "sha256"),
		filepath.Join(d.Containers, "manifests"),
		filepath.Join(d.CacheOCI, "blobs", "sha256"),
		filepath.Join(d.CacheOCI, "manifests"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	return nil
}

// StatusFile is the daemon heartbeat JSON path.
func (d Dirs) StatusFile() string {
	return filepath.Join(d.State, "status.json")
}

// ConfigFile is the copied runtime config snapshot.
func (d Dirs) ConfigFile() string {
	return filepath.Join(d.State, "config.toml")
}

// IndexFile is the pinned container image index.
func (d Dirs) IndexFile() string {
	return filepath.Join(d.Containers, "index.db")
}

// CacheIndexFile is the pull-through cache index.
func (d Dirs) CacheIndexFile() string {
	return filepath.Join(d.CacheOCI, "index.db")
}

// RunnerManifest is the SHA256 manifest for baked/imported runner images.
func (d Dirs) RunnerManifest() string {
	return filepath.Join(d.Runner, "MANIFEST")
}
