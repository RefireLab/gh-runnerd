package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/RefireLab/gh-runnerd/internal/layout"
)

var (
	ErrNotFound      = errors.New("image not found")
	ErrExists        = errors.New("image already exists")
	ErrQuotaExceeded = errors.New("disk quota exceeded")
	ErrPushDisabled  = errors.New("registry is pull-only")
)

// Record is one tagged image in a store.
type Record struct {
	Name      string    `json:"name"`
	Tag       string    `json:"tag"`
	Digest    string    `json:"digest"`
	MediaType string    `json:"media_type"`
	Size      int64     `json:"size"`
	Pinned    bool      `json:"pinned"`
	Imported  bool      `json:"imported"`
	CreatedAt time.Time `json:"created_at"`
	Pool      string    `json:"pool,omitempty"`
}

// Index is the on-disk image catalog.
type Index struct {
	Images []Record `json:"images"`
}

// Store is a filesystem OCI blob+manifest store.
type Store struct {
	root      string
	indexPath string
	quota     int64
	pinned    bool
	mu        sync.Mutex
}

// OpenPinned opens the immutable/imported image store.
func OpenPinned(dirs layout.Dirs, quota int64) *Store {
	return &Store{
		root:      dirs.Containers,
		indexPath: dirs.IndexFile(),
		quota:     quota,
		pinned:    true,
	}
}

// OpenCache opens the evictable pull-through cache.
func OpenCache(dirs layout.Dirs, quota int64) *Store {
	return &Store{
		root:      dirs.CacheOCI,
		indexPath: dirs.CacheIndexFile(),
		quota:     quota,
		pinned:    false,
	}
}

func (s *Store) blobsDir() string {
	return filepath.Join(s.root, "blobs", "sha256")
}

func (s *Store) blobPath(digest string) string {
	hexPart := strings.TrimPrefix(digest, "sha256:")
	return filepath.Join(s.blobsDir(), hexPart)
}

func (s *Store) lock() {
	s.mu.Lock()
}

func (s *Store) unlock() {
	s.mu.Unlock()
}

func (s *Store) loadIndex() (Index, error) {
	raw, err := os.ReadFile(s.indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Index{}, nil
		}
		return Index{}, err
	}
	var idx Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		return Index{}, err
	}
	return idx, nil
}

func (s *Store) saveIndex(idx Index) error {
	if err := os.MkdirAll(filepath.Dir(s.indexPath), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.indexPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.indexPath)
}

// List returns records, optionally filtered by pool.
func (s *Store) List(pool string) ([]Record, error) {
	s.lock()
	defer s.unlock()
	idx, err := s.loadIndex()
	if err != nil {
		return nil, err
	}
	if pool == "" {
		out := append([]Record(nil), idx.Images...)
		sort.Slice(out, func(i, j int) bool {
			if out[i].Name == out[j].Name {
				return out[i].Tag < out[j].Tag
			}
			return out[i].Name < out[j].Name
		})
		return out, nil
	}
	var out []Record
	for _, r := range idx.Images {
		if r.Pool == pool {
			out = append(out, r)
		}
	}
	return out, nil
}

// Lookup finds a name:tag in an optional pool. Empty pool matches the default.
func (s *Store) Lookup(name, tag, pool string) (Record, error) {
	s.lock()
	defer s.unlock()
	return s.lookupLocked(name, tag, pool)
}

func (s *Store) lookupLocked(name, tag, pool string) (Record, error) {
	idx, err := s.loadIndex()
	if err != nil {
		return Record{}, err
	}
	for _, r := range idx.Images {
		if r.Name == name && r.Tag == tag && r.Pool == pool {
			return r, nil
		}
	}
	return Record{}, ErrNotFound
}

// LookupDigest finds a manifest by digest.
func (s *Store) LookupDigest(name, digest, pool string) (Record, error) {
	s.lock()
	defer s.unlock()
	idx, err := s.loadIndex()
	if err != nil {
		return Record{}, err
	}
	for _, r := range idx.Images {
		if r.Name == name && r.Digest == digest && r.Pool == pool {
			return r, nil
		}
	}
	return Record{}, ErrNotFound
}

// ReadBlob returns blob bytes.
func (s *Store) ReadBlob(digest string) ([]byte, error) {
	b, err := os.ReadFile(s.blobPath(digest))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return b, nil
}

// HasBlob reports whether digest is stored.
func (s *Store) HasBlob(digest string) bool {
	_, err := os.Stat(s.blobPath(digest))
	return err == nil
}

// PutBlob writes content-addressed bytes. Existing blobs are left untouched.
func (s *Store) PutBlob(data []byte) (string, error) {
	s.lock()
	defer s.unlock()
	return s.putBlobLocked(data)
}

func (s *Store) putBlobLocked(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	path := s.blobPath(digest)
	if _, err := os.Stat(path); err == nil {
		return digest, nil
	}
	used, err := s.dirSizeLocked()
	if err != nil {
		return "", err
	}
	if s.quota > 0 && used+int64(len(data)) > s.quota {
		return "", fmt.Errorf("%w: %d bytes used, quota %d", ErrQuotaExceeded, used, s.quota)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return digest, nil
}

func (s *Store) dirSizeLocked() (int64, error) {
	var total int64
	err := filepath.Walk(s.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// Size returns current bytes used.
func (s *Store) Size() (int64, error) {
	s.lock()
	defer s.unlock()
	return s.dirSizeLocked()
}

// PutManifest records a tag pointing at a stored manifest blob.
func (s *Store) PutManifest(rec Record) error {
	s.lock()
	defer s.unlock()
	if rec.Name == "" || rec.Tag == "" || rec.Digest == "" {
		return fmt.Errorf("incomplete image record")
	}
	idx, err := s.loadIndex()
	if err != nil {
		return err
	}
	for i, existing := range idx.Images {
		if existing.Name == rec.Name && existing.Tag == rec.Tag && existing.Pool == rec.Pool {
			if s.pinned {
				return fmt.Errorf("%w: %s:%s (jobs cannot replace pinned images)", ErrExists, rec.Name, rec.Tag)
			}
			idx.Images[i] = rec
			return s.saveIndex(idx)
		}
	}
	idx.Images = append(idx.Images, rec)
	return s.saveIndex(idx)
}

// Remove deletes a tag. Blobs are left for GC.
func (s *Store) Remove(name, tag, pool string) error {
	s.lock()
	defer s.unlock()
	idx, err := s.loadIndex()
	if err != nil {
		return err
	}
	found := false
	var keep []Record
	for _, r := range idx.Images {
		if r.Name == name && r.Tag == tag && r.Pool == pool {
			found = true
			continue
		}
		keep = append(keep, r)
	}
	if !found {
		return ErrNotFound
	}
	idx.Images = keep
	return s.saveIndex(idx)
}

// ReferencedDigests returns all blob digests still named by the index plus
// whatever extra the caller already knows (e.g. config/layer digests parsed
// from manifests).
func (s *Store) ReferencedDigests() (map[string]struct{}, error) {
	s.lock()
	defer s.unlock()
	idx, err := s.loadIndex()
	if err != nil {
		return nil, err
	}
	refs := map[string]struct{}{}
	for _, r := range idx.Images {
		refs[r.Digest] = struct{}{}
		data, err := os.ReadFile(s.blobPath(r.Digest))
		if err != nil {
			continue
		}
		for _, d := range parseReferencedDigests(data) {
			refs[d] = struct{}{}
		}
	}
	return refs, nil
}

func parseReferencedDigests(manifest []byte) []string {
	var raw struct {
		Config *struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(manifest, &raw); err != nil {
		return nil
	}
	var out []string
	if raw.Config != nil && raw.Config.Digest != "" {
		out = append(out, raw.Config.Digest)
	}
	for _, l := range raw.Layers {
		if l.Digest != "" {
			out = append(out, l.Digest)
		}
	}
	for _, m := range raw.Manifests {
		if m.Digest != "" {
			out = append(out, m.Digest)
		}
	}
	return out
}

// Prune deletes unreferenced blobs. dryRun reports what would be removed.
func (s *Store) Prune(dryRun bool) (removed []string, bytesFreed int64, err error) {
	refs, err := s.ReferencedDigests()
	if err != nil {
		return nil, 0, err
	}
	s.lock()
	defer s.unlock()
	dir := s.blobsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		digest := "sha256:" + e.Name()
		if _, ok := refs[digest]; ok {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		removed = append(removed, digest)
		bytesFreed += info.Size()
		if !dryRun {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(removed)
	return removed, bytesFreed, nil
}

// CopyBlobTo writes a blob to w.
func (s *Store) CopyBlobTo(digest string, w io.Writer) (int64, error) {
	f, err := os.Open(s.blobPath(digest))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	defer f.Close()
	return io.Copy(w, f)
}
