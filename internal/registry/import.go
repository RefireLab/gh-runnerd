package registry

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// ImportResult is printed by `gh-runnerd image import`.
type ImportResult struct {
	Name      string
	Tag       string
	Digest    string
	Reference string
}

// ImportTar loads a docker save or OCI layout archive into the pinned store.
func (s *Store) ImportTar(path, name, tag, pool string) (ImportResult, error) {
	img, mediaType, err := loadTarImage(path)
	if err != nil {
		return ImportResult{}, err
	}
	return s.PutImage(img, mediaType, name, tag, pool, true)
}

// PutImage writes a v1.Image into the store and tags it.
func (s *Store) PutImage(img v1.Image, mediaType string, name, tag, pool string, imported bool) (ImportResult, error) {
	if name == "" || tag == "" {
		return ImportResult{}, fmt.Errorf("name and tag are required")
	}
	digest, err := img.Digest()
	if err != nil {
		return ImportResult{}, err
	}
	raw, err := img.RawManifest()
	if err != nil {
		return ImportResult{}, err
	}
	if mediaType == "" {
		mt, err := img.MediaType()
		if err != nil {
			return ImportResult{}, err
		}
		mediaType = string(mt)
	}
	if _, err := s.PutBlob(raw); err != nil {
		return ImportResult{}, err
	}
	cfg, err := img.RawConfigFile()
	if err != nil {
		return ImportResult{}, err
	}
	if _, err := s.PutBlob(cfg); err != nil {
		return ImportResult{}, err
	}
	layers, err := img.Layers()
	if err != nil {
		return ImportResult{}, err
	}
	var total int64 = int64(len(raw) + len(cfg))
	for _, l := range layers {
		rc, err := l.Compressed()
		if err != nil {
			return ImportResult{}, err
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return ImportResult{}, err
		}
		if _, err := s.PutBlob(data); err != nil {
			return ImportResult{}, err
		}
		total += int64(len(data))
	}
	rec := Record{
		Name:      name,
		Tag:       tag,
		Digest:    digest.String(),
		MediaType: mediaType,
		Size:      total,
		Pinned:    s.pinned,
		Imported:  imported,
		CreatedAt: time.Now().UTC(),
		Pool:      pool,
	}
	if err := s.PutManifest(rec); err != nil {
		return ImportResult{}, err
	}
	return ImportResult{
		Name:      name,
		Tag:       tag,
		Digest:    digest.String(),
		Reference: fmt.Sprintf("gh-runnerd.local/%s:%s", name, tag),
	}, nil
}

func loadTarImage(path string) (v1.Image, string, error) {
	kind, err := sniffTar(path)
	if err != nil {
		return nil, "", err
	}
	switch kind {
	case "oci":
		tmp, err := os.MkdirTemp("", "gh-runnerd-oci-*")
		if err != nil {
			return nil, "", err
		}
		defer os.RemoveAll(tmp)
		if err := extractTar(path, tmp); err != nil {
			return nil, "", err
		}
		idx, err := layout.ImageIndexFromPath(tmp)
		if err != nil {
			return nil, "", fmt.Errorf("oci layout: %w", err)
		}
		mf, err := idx.IndexManifest()
		if err != nil {
			return nil, "", err
		}
		if len(mf.Manifests) == 0 {
			return nil, "", fmt.Errorf("oci layout has no manifests")
		}
		img, err := idx.Image(mf.Manifests[0].Digest)
		if err != nil {
			return nil, "", err
		}
		mt, err := img.MediaType()
		if err != nil {
			return nil, "", err
		}
		return img, string(mt), nil
	default:
		img, err := tarball.ImageFromPath(path, nil)
		if err != nil {
			return nil, "", fmt.Errorf("docker tar: %w", err)
		}
		mt, err := img.MediaType()
		if err != nil {
			return nil, "", err
		}
		return img, string(mt), nil
	}
}

func sniffTar(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if name == "oci-layout" || name == "index.json" {
			return "oci", nil
		}
		if name == "manifest.json" {
			return "docker", nil
		}
	}
	return "docker", nil
}

func extractTar(path, dest string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		target := dest + "/" + name
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
}

// InspectJSON returns a printable record plus digest verification.
type InspectJSON struct {
	Record
	Reference     string `json:"reference"`
	DigestOK      bool   `json:"digest_ok"`
	ManifestBytes int    `json:"manifest_bytes"`
}

// Inspect loads and verifies a tagged image.
func (s *Store) Inspect(name, tag, pool string) (InspectJSON, error) {
	rec, err := s.Lookup(name, tag, pool)
	if err != nil {
		return InspectJSON{}, err
	}
	raw, err := s.ReadBlob(rec.Digest)
	if err != nil {
		return InspectJSON{}, err
	}
	sum := sha256.Sum256(raw)
	ok := rec.Digest == "sha256:"+hex.EncodeToString(sum[:])
	return InspectJSON{
		Record:        rec,
		Reference:     fmt.Sprintf("gh-runnerd.local/%s:%s", rec.Name, rec.Tag),
		DigestOK:      ok,
		ManifestBytes: len(raw),
	}, nil
}
