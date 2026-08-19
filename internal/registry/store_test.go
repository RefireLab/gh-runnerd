package registry

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/RefireLab/gh-runnerd/internal/layout"
)

func testStores(t *testing.T) (*Store, *Store) {
	t.Helper()
	dirs := layout.New(t.TempDir())
	if err := dirs.Ensure(); err != nil {
		t.Fatal(err)
	}
	return OpenPinned(dirs, 50*1024*1024), OpenCache(dirs, 50*1024*1024)
}

func TestImportDockerTarAndPull(t *testing.T) {
	t.Parallel()
	pinned, cache := testStores(t)
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	tarPath := filepath.Join(t.TempDir(), "img.tar")
	tag, err := name.NewTag("my-ci:2026.08")
	if err != nil {
		t.Fatal(err)
	}
	if err := tarball.WriteToFile(tarPath, tag, img); err != nil {
		t.Fatal(err)
	}
	res, err := pinned.ImportTar(tarPath, "my-ci", "2026.08", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Reference != "gh-runnerd.local/my-ci:2026.08" {
		t.Fatalf("ref %s", res.Reference)
	}
	if !strings.HasPrefix(res.Digest, "sha256:") {
		t.Fatalf("digest %s", res.Digest)
	}
	insp, err := pinned.Inspect("my-ci", "2026.08", "")
	if err != nil {
		t.Fatal(err)
	}
	if !insp.DigestOK {
		t.Fatal("digest mismatch after import")
	}

	srv := &Server{Pinned: pinned, Cache: cache}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v2/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("v2 status %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/v2/my-ci/manifests/2026.08", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("manifest status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Docker-Content-Digest"); got != res.Digest {
		t.Fatalf("digest header %s want %s", got, res.Digest)
	}

	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/v2/my-ci/manifests/evil", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("push status %d", resp.StatusCode)
	}
}

func TestPinnedImageCannotBeReplaced(t *testing.T) {
	t.Parallel()
	pinned, _ := testStores(t)
	img, err := random.Image(128, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pinned.PutImage(img, "", "my-ci", "1", "", true); err != nil {
		t.Fatal(err)
	}
	img2, err := random.Image(128, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pinned.PutImage(img2, "", "my-ci", "1", "", true)
	if err == nil {
		t.Fatal("expected replace to fail")
	}
}

func TestQuotaExceeded(t *testing.T) {
	t.Parallel()
	dirs := layout.New(t.TempDir())
	if err := dirs.Ensure(); err != nil {
		t.Fatal(err)
	}
	s := OpenPinned(dirs, 32)
	if _, err := s.PutBlob([]byte("this-is-definitely-over-the-tiny-quota-limit")); err == nil {
		t.Fatal("expected quota error")
	}
}

func TestPruneDryRunAndRemove(t *testing.T) {
	t.Parallel()
	pinned, _ := testStores(t)
	d1, err := pinned.PutBlob([]byte("keep-me-layer-aaaaaaaa"))
	if err != nil {
		t.Fatal(err)
	}
	d2, err := pinned.PutBlob([]byte("orphan-blob-bbbbbbbbbb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := pinned.PutManifest(Record{Name: "x", Tag: "1", Digest: d1, MediaType: "application/json"}); err != nil {
		t.Fatal(err)
	}
	removed, _, err := pinned.Prune(true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range removed {
		if d == d2 {
			found = true
		}
		if d == d1 {
			t.Fatal("must not prune referenced blob")
		}
	}
	if !found {
		t.Fatalf("expected orphan %s in dry-run, got %v", d2, removed)
	}
	if _, _, err := pinned.Prune(false); err != nil {
		t.Fatal(err)
	}
	if pinned.HasBlob(d2) {
		t.Fatal("orphan still present")
	}
	if !pinned.HasBlob(d1) {
		t.Fatal("referenced blob gone")
	}
}

func TestParseLocalRef(t *testing.T) {
	t.Parallel()
	got, err := ParseLocalRef("gh-runnerd.local/my-ci:2026.08")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "my-ci" || got.Tag != "2026.08" {
		t.Fatalf("%+v", got)
	}
}

func TestImportMissingFile(t *testing.T) {
	t.Parallel()
	pinned, _ := testStores(t)
	_, err := pinned.ImportTar(filepath.Join(t.TempDir(), "nope.tar"), "x", "1", "")
	if err == nil {
		t.Fatal("expected error")
	}
	_ = os.ErrNotExist
}
