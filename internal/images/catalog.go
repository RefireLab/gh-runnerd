package images

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// RunnerImage is a bootable Ubuntu runner VM template.
type RunnerImage struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	SHA256    string    `json:"sha256"`
	Arch      string    `json:"arch"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	Size      int64     `json:"size"`
}

type manifestFile struct {
	Images []RunnerImage `json:"images"`
	Active string        `json:"active"`
}

// Catalog manages runner qcow2 templates in the data dir (images/runner).
type Catalog struct {
	Dir string
}

func (c Catalog) manifestPath() string {
	return filepath.Join(c.Dir, "MANIFEST")
}

func (c Catalog) load() (manifestFile, error) {
	raw, err := os.ReadFile(c.manifestPath())
	if err != nil {
		if os.IsNotExist(err) {
			return manifestFile{}, nil
		}
		return manifestFile{}, err
	}
	var m manifestFile
	if err := json.Unmarshal(raw, &m); err != nil {
		return manifestFile{}, err
	}
	return m, nil
}

func (c Catalog) save(m manifestFile) error {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.manifestPath(), raw, 0o600)
}

// List returns known runner images.
func (c Catalog) List() ([]RunnerImage, error) {
	m, err := c.load()
	if err != nil {
		return nil, err
	}
	for i := range m.Images {
		m.Images[i].Active = m.Images[i].Name == m.Active
	}
	return m.Images, nil
}

// Active returns the currently activated template.
func (c Catalog) Active() (RunnerImage, error) {
	m, err := c.load()
	if err != nil {
		return RunnerImage{}, err
	}
	if m.Active == "" {
		return RunnerImage{}, fmt.Errorf("no runner image is active; bake or import one first (gh-runnerd runner-image bake)")
	}
	for _, img := range m.Images {
		if img.Name == m.Active {
			img.Active = true
			return img, nil
		}
	}
	return RunnerImage{}, fmt.Errorf("active runner image %q missing from MANIFEST", m.Active)
}

// familyNameRe matches bare upstream family names like ubuntu-24.04.
var familyNameRe = regexp.MustCompile(`^ubuntu-\d{2}\.\d{2}$`)

// Import copies a qcow2 into the catalog, checksums it, and records it.
func (c Catalog) Import(src, name string) (RunnerImage, error) {
	if name == "" {
		return RunnerImage{}, fmt.Errorf("name is required")
	}
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return RunnerImage{}, err
	}
	dst := filepath.Join(c.Dir, name+".qcow2")
	if err := copyFile(src, dst); err != nil {
		return RunnerImage{}, err
	}
	sum, size, err := hashFile(dst)
	if err != nil {
		return RunnerImage{}, err
	}
	img := RunnerImage{
		Name:      name,
		Path:      dst,
		SHA256:    sum,
		Arch:      runtime.GOARCH,
		CreatedAt: time.Now().UTC(),
		Size:      size,
	}
	m, err := c.load()
	if err != nil {
		return RunnerImage{}, err
	}
	replaced := false
	for i, existing := range m.Images {
		if existing.Name == name {
			m.Images[i] = img
			replaced = true
			break
		}
	}
	if !replaced {
		m.Images = append(m.Images, img)
	}
	if m.Active == "" {
		m.Active = name
	}
	if err := c.save(m); err != nil {
		return RunnerImage{}, err
	}
	return img, nil
}

// Activate marks name as the default template.
func (c Catalog) Activate(name string) error {
	m, err := c.load()
	if err != nil {
		return err
	}
	for _, img := range m.Images {
		if img.Name == name {
			m.Active = name
			return c.save(m)
		}
	}
	return fmt.Errorf("runner image %q not found", name)
}

// Validate checks the qcow2 exists, checksum matches, and qemu-img can read it.
func (c Catalog) Validate(name string) error {
	m, err := c.load()
	if err != nil {
		return err
	}
	var img *RunnerImage
	for i := range m.Images {
		if m.Images[i].Name == name {
			img = &m.Images[i]
			break
		}
	}
	if img == nil {
		return fmt.Errorf("runner image %q not found", name)
	}
	st, err := os.Stat(img.Path)
	if err != nil {
		return fmt.Errorf("runner image file: %w", err)
	}
	if st.Size() < 1024 {
		return fmt.Errorf("runner image %s is too small to be a bootable disk", img.Path)
	}
	sum, _, err := hashFile(img.Path)
	if err != nil {
		return err
	}
	if img.SHA256 != "" && img.SHA256 != sum {
		return fmt.Errorf("SHA256 mismatch for %s: manifest %s disk %s", name, img.SHA256, sum)
	}
	if _, err := exec.LookPath("qemu-img"); err == nil {
		out, err := exec.Command("qemu-img", "info", "--output=json", img.Path).CombinedOutput()
		if err != nil {
			return fmt.Errorf("qemu-img info: %s", out)
		}
		if !strings.Contains(string(out), "qcow2") && !strings.Contains(string(out), "raw") {
			return fmt.Errorf("qemu-img did not recognize %s as a disk image", img.Path)
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Find looks up a named template, mapping ubuntu-24.04 to the arch-specific file.
func (c Catalog) Find(name string) (RunnerImage, error) {
	// Bare family names (vm.template = "ubuntu-24.04") resolve to the
	// arch-suffixed template baked on this host.
	if familyNameRe.MatchString(name) {
		name = name + "-" + runtime.GOARCH
	}
	list, err := c.List()
	if err != nil {
		return RunnerImage{}, err
	}
	for _, img := range list {
		if img.Name == name {
			return img, nil
		}
	}
	// Fall back to a raw file sitting in the directory (fresh bake, no MANIFEST yet).
	p := filepath.Join(c.Dir, name+".qcow2")
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return RunnerImage{Name: name, Path: p, Size: st.Size()}, nil
	}
	return RunnerImage{}, fmt.Errorf("runner image %q not found in %s", name, c.Dir)
}
