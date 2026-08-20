package runnerimages

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LatestReleases returns the newest upstream release tag per Ubuntu family
// for the given arch, e.g. {"ubuntu-24.04": "ubuntu24/20260816.277"}.
// GitHub lists releases newest-first, so the first prefix match wins.
// token is optional; it only raises the API rate limit.
func LatestReleases(ctx context.Context, token, arch string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+Repo+"/releases?per_page=100", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list %s releases: %w", Repo, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list %s releases: %s", Repo, resp.Status)
	}
	var releases []struct {
		TagName    string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("parse releases: %w", err)
	}
	tags := make([]string, 0, len(releases))
	for _, r := range releases {
		if !r.Prerelease {
			tags = append(tags, r.TagName)
		}
	}
	return matchLatestTags(tags, arch), nil
}

// matchLatestTags picks the first (newest) tag per family from a
// newest-first tag list.
func matchLatestTags(tags []string, arch string) map[string]string {
	out := map[string]string{}
	for _, family := range KnownFamilies() {
		prefix := TagPrefix(family, arch)
		for _, t := range tags {
			if strings.HasPrefix(t, prefix) {
				out[family] = t
				break
			}
		}
	}
	return out
}

// LatestReleaseTag resolves the newest release tag for one family.
func LatestReleaseTag(ctx context.Context, token, family, arch string) (string, error) {
	tags, err := LatestReleases(ctx, token, arch)
	if err != nil {
		return "", err
	}
	tag, ok := tags[family]
	if !ok {
		return "", fmt.Errorf("no %s release found for %s", Repo, family)
	}
	return tag, nil
}

// ReleaseVersion extracts "20260816.277" from "ubuntu24/20260816.277".
// Branch refs ("main") return "dev".
func ReleaseVersion(ref string) string {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return "dev"
}

// Fetch downloads and extracts the runner-images source tree at ref into
// cacheDir, returning the tree root (the directory that contains images/
// and helpers/). Tags contain a slash (ubuntu24/20260816.277); anything
// without one is treated as a branch. Extractions are cached by ref.
func Fetch(ctx context.Context, cacheDir, ref string, fresh bool, say func(string, ...any)) (string, error) {
	key := strings.NewReplacer("/", "_", ":", "_").Replace(ref)
	root := filepath.Join(cacheDir, key)
	marker := filepath.Join(root, ".gh-runnerd-complete")
	if !fresh {
		if _, err := os.Stat(marker); err == nil {
			say(">> using cached %s@%s", Repo, ref)
			return root, nil
		}
	}
	if err := os.RemoveAll(root); err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}

	kind := "tags"
	if !strings.Contains(ref, "/") {
		kind = "heads"
	}
	url := "https://codeload.github.com/" + Repo + "/tar.gz/refs/" + kind + "/" + ref
	say(">> downloading %s@%s (image build scripts)", Repo, ref)
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", url, resp.Status)
	}
	if err := extractTarGz(resp.Body, root); err != nil {
		return "", fmt.Errorf("extract %s: %w", url, err)
	}
	if _, err := os.Stat(filepath.Join(root, "images", "ubuntu")); err != nil {
		return "", fmt.Errorf("%s@%s does not contain images/ubuntu — wrong ref?", Repo, ref)
	}
	if err := os.WriteFile(marker, []byte(ref+"\n"), 0o644); err != nil {
		return "", err
	}
	return root, nil
}

// extractTarGz unpacks a GitHub codeload tarball, stripping the leading
// "<repo>-<ref>/" path element and refusing entries that escape dst.
func extractTarGz(r io.Reader, dst string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := hdr.Name
		if i := strings.IndexByte(name, '/'); i >= 0 {
			name = name[i+1:]
		} else {
			continue // the top-level directory entry itself
		}
		if name == "" {
			continue
		}
		clean := filepath.Clean(name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("tar entry escapes destination: %s", hdr.Name)
		}
		target := filepath.Join(dst, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if hdr.FileInfo().Mode()&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		default:
			// Symlinks and specials are not needed from this repo.
		}
	}
}
