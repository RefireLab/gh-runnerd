package images

import (
	"strings"
	"testing"
)

func TestParseRunnerfile(t *testing.T) {
	t.Parallel()
	src := `
FROM gh-runnerd/ubuntu-24.04
# extra tools
RUN apt-get update && apt-get install -y ffmpeg imagemagick
PRELOAD ghcr.io/company/ci:2026.08
PRELOAD alpine:3.22
`
	rf, err := ParseRunnerfile(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if rf.From != "gh-runnerd/ubuntu-24.04" {
		t.Fatalf("from %s", rf.From)
	}
	if len(rf.Runs) != 1 || len(rf.Preloads) != 2 {
		t.Fatalf("runs=%v preloads=%v", rf.Runs, rf.Preloads)
	}
}

func TestParseRunnerfileRejectsAlpineBase(t *testing.T) {
	t.Parallel()
	_, err := ParseRunnerfile(strings.NewReader("FROM alpine:3.22\n"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Ubuntu 24.04") {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestParseRunnerfileRejectsUnknownOp(t *testing.T) {
	t.Parallel()
	_, err := ParseRunnerfile(strings.NewReader("FROM gh-runnerd/ubuntu-24.04\nCOPY . .\n"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseRunnerfileRequiresFrom(t *testing.T) {
	t.Parallel()
	_, err := ParseRunnerfile(strings.NewReader("RUN echo hi\n"))
	if err == nil {
		t.Fatal("expected error")
	}
}
