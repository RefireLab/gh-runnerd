package daemon

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RefireLab/gh-runnerd/internal/config"
	"github.com/RefireLab/gh-runnerd/internal/githubutil"
)

func TestWebhookRejectsBadSignature(t *testing.T) {
	d := &Daemon{Cfg: config.Defaults()}
	d.Cfg.Webhook.Secret = "s3cret"
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader([]byte(`{"action":"queued"}`)))
	req.Header.Set("X-Hub-Signature-256", "sha256=nope")
	rec := httptest.NewRecorder()
	d.handleWebhook(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestWebhookAcceptsQueuedWorkflowJob(t *testing.T) {
	d := &Daemon{Cfg: config.Defaults()}
	d.Cfg.Webhook.Secret = "s3cret"
	body := []byte(`{"action":"queued","workflow_job":{"id":1,"labels":["ubuntu-latest"]}}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", githubutil.SignBody("s3cret", body))
	req.Header.Set("X-GitHub-Event", "workflow_job")
	rec := httptest.NewRecorder()
	d.handleWebhook(rec, req)
	if rec.Code != 202 {
		t.Fatalf("status %d", rec.Code)
	}
}
