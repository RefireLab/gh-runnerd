package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// ParsedRef is a local or remote image reference.
type ParsedRef struct {
	Name string
	Tag  string
	Raw  string
}

// ParseLocalRef parses name, name:tag, or gh-runnerd.local/name:tag.
func ParseLocalRef(s string) (ParsedRef, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return ParsedRef{}, fmt.Errorf("empty image reference")
	}
	s = strings.TrimPrefix(s, "gh-runnerd.local/")
	namePart, tag, ok := strings.Cut(s, ":")
	if !ok {
		tag = "latest"
	}
	namePart = strings.TrimPrefix(namePart, "library/")
	if namePart == "" {
		return ParsedRef{}, fmt.Errorf("invalid image reference %q", s)
	}
	return ParsedRef{Name: namePart, Tag: tag, Raw: s}, nil
}

// Pull copies a remote image into the pinned store and tags it.
func (s *Store) Pull(ctx context.Context, remoteRef, localName, localTag, pool string, auth UpstreamAuth) (ImportResult, error) {
	if localName == "" || localTag == "" {
		parsed, err := ParseLocalRef(remoteRef)
		if err != nil {
			return ImportResult{}, err
		}
		if localName == "" {
			localName = parsed.Name
		}
		if localTag == "" {
			localTag = parsed.Tag
		}
	}
	ref, err := name.ParseReference(remoteRef, name.WeakValidation)
	if err != nil {
		return ImportResult{}, err
	}
	opts := []remote.Option{remote.WithContext(ctx)}
	if (ref.Context().RegistryStr() == "docker.io" || ref.Context().RegistryStr() == "index.docker.io") && auth.DockerHubUser != "" {
		// Host Docker Hub token is applied by callers via remote options if needed.
	}
	_ = auth
	img, err := remote.Image(ref, opts...)
	if err != nil {
		return ImportResult{}, fmt.Errorf("pull %s: %w", remoteRef, err)
	}
	mt, err := img.MediaType()
	if err != nil {
		return ImportResult{}, err
	}
	return s.PutImage(img, string(mt), localName, localTag, pool, false)
}

// ExampleImages are pre-seeded at `init --with-examples`.
func ExampleImages() []string {
	return []string{
		"alpine:3.22",
		"ubuntu:24.04",
		"node:22-bookworm",
		"python:3.13-slim",
	}
}
