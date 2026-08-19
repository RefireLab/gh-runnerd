package registry

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// UpstreamAuth is host-level credentials used only for anonymous-looking pulls
// (Docker Hub rate limits). Job Authorization headers are proxied and never cached.
type UpstreamAuth struct {
	DockerHubUser  string
	DockerHubToken string
}

// Server is the pull-only OCI distribution endpoint.
type Server struct {
	Pinned *Store
	Cache  *Store
	Auth   UpstreamAuth
	Pool   string
	Log    *slog.Logger
	Client *http.Client
}

func (s *Server) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Server) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

// Handler returns the OCI distribution HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", s.serveV2)
	return s.denyPush(mux)
}

func (s *Server) denyPush(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte(`{"errors":[{"code":"DENIED","message":"gh-runnerd registry is pull-only"}]}`))
		}
	})
}

func (s *Server) serveV2(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v2/")
	if path == "" || path == "/" {
		w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
		return
	}
	name, rest, ok := splitName(path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasPrefix(rest, "manifests/"):
		s.serveManifest(w, r, name, strings.TrimPrefix(rest, "manifests/"))
	case strings.HasPrefix(rest, "blobs/"):
		s.serveBlob(w, r, name, strings.TrimPrefix(rest, "blobs/"))
	case rest == "tags/list":
		s.serveTags(w, r, name)
	default:
		http.NotFound(w, r)
	}
}

func splitName(path string) (name, rest string, ok bool) {
	// /v2/<name>/manifests/<ref> where name may contain slashes.
	idx := strings.LastIndex(path, "/manifests/")
	if idx >= 0 {
		return path[:idx], path[idx+1:], true
	}
	idx = strings.LastIndex(path, "/blobs/")
	if idx >= 0 {
		return path[:idx], path[idx+1:], true
	}
	idx = strings.LastIndex(path, "/tags/list")
	if idx >= 0 {
		return path[:idx], path[idx+1:], true
	}
	return "", "", false
}

func (s *Server) serveManifest(w http.ResponseWriter, r *http.Request, imageName, ref string) {
	authenticated := r.Header.Get("Authorization") != ""
	rec, store, err := s.findManifest(imageName, ref)
	if err == nil {
		raw, err := store.ReadBlob(rec.Digest)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", rec.MediaType)
		w.Header().Set("Docker-Content-Digest", rec.Digest)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(raw)))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(raw)
		}
		return
	}
	upstream := upstreamForHost(r.Host)
	if upstream == "" {
		http.Error(w, `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`, http.StatusNotFound)
		return
	}
	imgRef := upstream + "/" + imageName + ":" + ref
	if strings.HasPrefix(ref, "sha256:") {
		imgRef = upstream + "/" + imageName + "@" + ref
	}
	raw, digest, mediaType, err := s.fetchManifest(r.Context(), imgRef, r.Header.Get("Authorization"))
	if err != nil {
		s.logger().Warn("upstream manifest fetch failed", "ref", imgRef, "err", err)
		http.Error(w, `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`, http.StatusNotFound)
		return
	}
	if !authenticated {
		if _, err := s.Cache.PutBlob(raw); err == nil {
			_ = s.Cache.PutManifest(Record{
				Name:      imageName,
				Tag:       tagFromRef(ref, digest),
				Digest:    digest,
				MediaType: mediaType,
				Size:      int64(len(raw)),
				Pinned:    false,
				CreatedAt: time.Now().UTC(),
				Pool:      s.Pool,
			})
		}
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(raw)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(raw)
	}
}

func tagFromRef(ref, digest string) string {
	if strings.HasPrefix(ref, "sha256:") {
		return strings.TrimPrefix(digest, "sha256:")[:12]
	}
	return ref
}

func (s *Server) findManifest(imageName, ref string) (Record, *Store, error) {
	if strings.HasPrefix(ref, "sha256:") {
		if rec, err := s.Pinned.LookupDigest(imageName, ref, s.Pool); err == nil {
			return rec, s.Pinned, nil
		}
		if rec, err := s.Cache.LookupDigest(imageName, ref, s.Pool); err == nil {
			return rec, s.Cache, nil
		}
		return Record{}, nil, ErrNotFound
	}
	if rec, err := s.Pinned.Lookup(imageName, ref, s.Pool); err == nil {
		return rec, s.Pinned, nil
	}
	if rec, err := s.Cache.Lookup(imageName, ref, s.Pool); err == nil {
		return rec, s.Cache, nil
	}
	return Record{}, nil, ErrNotFound
}

func (s *Server) serveBlob(w http.ResponseWriter, r *http.Request, imageName, digest string) {
	authenticated := r.Header.Get("Authorization") != ""
	if s.Pinned.HasBlob(digest) {
		s.writeBlob(w, r, s.Pinned, digest)
		return
	}
	if s.Cache.HasBlob(digest) {
		s.writeBlob(w, r, s.Cache, digest)
		return
	}
	upstream := upstreamForHost(r.Host)
	if upstream == "" {
		http.Error(w, `{"errors":[{"code":"BLOB_UNKNOWN"}]}`, http.StatusNotFound)
		return
	}
	data, err := s.fetchBlob(r.Context(), upstream+"/"+imageName+"@"+digest, r.Header.Get("Authorization"))
	if err != nil {
		http.Error(w, `{"errors":[{"code":"BLOB_UNKNOWN"}]}`, http.StatusNotFound)
		return
	}
	if !authenticated {
		_, _ = s.Cache.PutBlob(data)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func (s *Server) writeBlob(w http.ResponseWriter, r *http.Request, store *Store, digest string) {
	raw, err := store.ReadBlob(digest)
	if err != nil {
		http.Error(w, `{"errors":[{"code":"BLOB_UNKNOWN"}]}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(raw)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(raw)
	}
}

func (s *Server) serveTags(w http.ResponseWriter, r *http.Request, imageName string) {
	seen := map[string]struct{}{}
	var tags []string
	add := func(list []Record, err error) {
		if err != nil {
			return
		}
		for _, rec := range list {
			if rec.Name != imageName || rec.Pool != s.Pool {
				continue
			}
			if _, ok := seen[rec.Tag]; ok {
				continue
			}
			seen[rec.Tag] = struct{}{}
			tags = append(tags, rec.Tag)
		}
	}
	pinned, _ := s.Pinned.List(s.Pool)
	cached, _ := s.Cache.List(s.Pool)
	add(pinned, nil)
	add(cached, nil)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"name":%q,"tags":[`, imageName)
	for i, t := range tags {
		if i > 0 {
			_, _ = io.WriteString(w, ",")
		}
		fmt.Fprintf(w, "%q", t)
	}
	_, _ = io.WriteString(w, "]}")
}

func upstreamForHost(host string) string {
	h := strings.ToLower(host)
	if i := strings.Index(h, ":"); i >= 0 {
		h = h[:i]
	}
	switch h {
	case "dockerhub.gh-runnerd.local", "registry-1.docker.io", "docker.io":
		return "docker.io"
	case "ghcr.gh-runnerd.local", "ghcr.io":
		return "ghcr.io"
	case "quay.gh-runnerd.local", "quay.io":
		return "quay.io"
	default:
		return ""
	}
}

func (s *Server) fetchManifest(ctx context.Context, ref, authorization string) ([]byte, string, string, error) {
	img, err := s.remoteImage(ctx, ref, authorization)
	if err != nil {
		return nil, "", "", err
	}
	raw, err := img.RawManifest()
	if err != nil {
		return nil, "", "", err
	}
	d, err := img.Digest()
	if err != nil {
		return nil, "", "", err
	}
	mt, err := img.MediaType()
	if err != nil {
		return nil, "", "", err
	}
	// Also pull config + layers into cache when unauthenticated, handled by caller of blobs.
	if authorization == "" {
		_ = s.cacheImageBlobs(img)
	}
	return raw, d.String(), string(mt), nil
}

func (s *Server) cacheImageBlobs(img v1.Image) error {
	cfg, err := img.RawConfigFile()
	if err != nil {
		return err
	}
	if _, err := s.Cache.PutBlob(cfg); err != nil {
		return err
	}
	layers, err := img.Layers()
	if err != nil {
		return err
	}
	for _, l := range layers {
		rc, err := l.Compressed()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return err
		}
		if _, err := s.Cache.PutBlob(data); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) fetchBlob(ctx context.Context, ref, authorization string) ([]byte, error) {
	d, err := name.NewDigest(ref)
	if err != nil {
		return nil, err
	}
	layer, err := remote.Layer(d, s.remoteOpts(ctx, authorization, d.RegistryStr())...)
	if err != nil {
		return nil, err
	}
	rc, err := layer.Compressed()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func (s *Server) remoteImage(ctx context.Context, ref, authorization string) (v1.Image, error) {
	parsed, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		return nil, err
	}
	return remote.Image(parsed, s.remoteOpts(ctx, authorization, parsed.Context().RegistryStr())...)
}

func (s *Server) remoteOpts(ctx context.Context, authorization, registry string) []remote.Option {
	opts := []remote.Option{remote.WithContext(ctx)}
	if authorization != "" {
		opts = append(opts, remote.WithAuthFromKeychain(headerKeychain{header: authorization}))
		return opts
	}
	if (registry == "docker.io" || registry == "index.docker.io") && s.Auth.DockerHubUser != "" && s.Auth.DockerHubToken != "" {
		opts = append(opts, remote.WithAuth(&authn.Basic{
			Username: s.Auth.DockerHubUser,
			Password: s.Auth.DockerHubToken,
		}))
	}
	return opts
}

type headerKeychain struct {
	header string
}

func (h headerKeychain) Resolve(_ authn.Resource) (authn.Authenticator, error) {
	return &headerAuth{header: h.header}, nil
}

type headerAuth struct {
	header string
}

func (h headerAuth) Authorization() (*authn.AuthConfig, error) {
	// Bearer or Basic is passed through via AuthConfig.Auth / IdentityToken as needed.
	val := strings.TrimSpace(h.header)
	if strings.HasPrefix(strings.ToLower(val), "bearer ") {
		return &authn.AuthConfig{RegistryToken: strings.TrimSpace(val[7:])}, nil
	}
	if strings.HasPrefix(strings.ToLower(val), "basic ") {
		return &authn.AuthConfig{Auth: strings.TrimSpace(val[6:])}, nil
	}
	return &authn.AuthConfig{RegistryToken: val}, nil
}
