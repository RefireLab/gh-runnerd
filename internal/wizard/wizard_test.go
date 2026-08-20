package wizard

import (
	"bytes"
	"strings"
	"testing"
)

func TestAskDefaultAndValue(t *testing.T) {
	p := New(strings.NewReader("\nhello\n"), &bytes.Buffer{})
	got, err := p.Ask("q1", "def")
	if err != nil || got != "def" {
		t.Fatalf("got %q err %v", got, err)
	}
	got, err = p.Ask("q2", "def")
	if err != nil || got != "hello" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestAskYesNo(t *testing.T) {
	p := New(strings.NewReader("\nn\nmaybe\nYES\n"), &bytes.Buffer{})
	if v, _ := p.AskYesNo("a", true); !v {
		t.Fatal("empty should return default true")
	}
	if v, _ := p.AskYesNo("b", true); v {
		t.Fatal("n should be false")
	}
	if v, _ := p.AskYesNo("c", false); !v {
		t.Fatal("re-ask then YES should be true")
	}
}

func TestAskInt(t *testing.T) {
	p := New(strings.NewReader("\nzero\n3\n"), &bytes.Buffer{})
	if v, _ := p.AskInt("a", 2); v != 2 {
		t.Fatalf("got %d", v)
	}
	if v, _ := p.AskInt("b", 2); v != 3 {
		t.Fatalf("got %d", v)
	}
}

func TestSanitizeInputStripsEscapes(t *testing.T) {
	// Arrow keys arrive as ESC[D etc.; a trailing bare ESC[ (cut off by
	// Enter) must not survive either. This exact byte pattern once ended
	// up inside github.poll_repos and broke every poll URL.
	in := "RefireLab/pitstop-ae\x1b[D\x1b[D\x1b[D\x1b[D\x1b[D\x1b["
	if got := sanitizeInput(in); got != "RefireLab/pitstop-ae" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeInput("plain-value"); got != "plain-value" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeInput("  padded \t"); got != "padded" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeInput("a\x1bOHb\x07c"); got != "abc" {
		t.Fatalf("got %q", got)
	}
}

func TestAskStripsArrowKeys(t *testing.T) {
	p := New(strings.NewReader("owner/repo\x1b[D\x1b[D\n"), &bytes.Buffer{})
	got, err := p.Ask("repo", "")
	if err != nil || got != "owner/repo" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestAskSecretNonTTY(t *testing.T) {
	p := New(strings.NewReader("tok123\n"), &bytes.Buffer{})
	if p.Interactive() {
		t.Fatal("string reader must not be interactive")
	}
	v, err := p.AskSecret("token")
	if err != nil || v != "tok123" {
		t.Fatalf("got %q err %v", v, err)
	}
}

func TestSelect(t *testing.T) {
	p := New(strings.NewReader("\n9\n2\n"), &bytes.Buffer{})
	if i, _ := p.Select("pick", []string{"a", "b"}, 0); i != 0 {
		t.Fatalf("default: got %d", i)
	}
	if i, _ := p.Select("pick", []string{"a", "b"}, 0); i != 1 {
		t.Fatalf("retry then 2: got %d", i)
	}
}
