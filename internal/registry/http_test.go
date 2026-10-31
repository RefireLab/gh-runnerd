package registry

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	ggcrregistry "github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// A digest-pinned pull of a multi-platform image (postgres:18@sha256:<index>)
// must get the index back with that same digest. Resolving it to one
// platform's image made Docker fail with "unexpected commit digest".
func TestFetchManifestKeepsMultiPlatformIndex(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(ggcrregistry.New())
	t.Cleanup(upstream.Close)
	host := strings.TrimPrefix(upstream.URL, "http://")

	idx, err := random.Index(256, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := name.ParseReference(host + "/library/multi:1")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, idx); err != nil {
		t.Fatal(err)
	}
	idxDigest, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	idxManifest, err := idx.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}

	pinned, cache := testStores(t)
	s := &Server{Pinned: pinned, Cache: cache}
	ctx := context.Background()

	for _, r := range []string{host + "/library/multi:1", host + "/library/multi@" + idxDigest.String()} {
		_, digest, mediaType, err := s.fetchManifest(ctx, r, "")
		if err != nil {
			t.Fatalf("%s: %v", r, err)
		}
		if digest != idxDigest.String() {
			t.Fatalf("%s: digest %s, want the index digest %s", r, digest, idxDigest)
		}
		if !types.MediaType(mediaType).IsIndex() {
			t.Fatalf("%s: media type %s, want an index", r, mediaType)
		}
	}

	// The client then asks for a platform's manifest by its own digest.
	child := idxManifest.Manifests[0].Digest
	_, digest, mediaType, err := s.fetchManifest(ctx, host+"/library/multi@"+child.String(), "")
	if err != nil {
		t.Fatal(err)
	}
	if digest != child.String() || !types.MediaType(mediaType).IsImage() {
		t.Fatalf("child: got %s %s, want %s image manifest", digest, mediaType, child)
	}
}
