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
