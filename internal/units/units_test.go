package units

import "testing"

func TestParseBytes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want int64
	}{
		{"50G", 50 * 1024 * 1024 * 1024},
		{"4G", 4 * 1024 * 1024 * 1024},
		{"4096M", 4096 * 1024 * 1024},
		{"512K", 512 * 1024},
		{"100", 100},
	}
	for _, tc := range cases {
		got, err := ParseBytes(tc.in)
		if err != nil {
			t.Fatalf("ParseBytes(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseBytes(%q)=%d want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseMB(t *testing.T) {
	t.Parallel()
	got, err := ParseMB("4G")
	if err != nil {
		t.Fatal(err)
	}
	if got != 4096 {
		t.Fatalf("got %d", got)
	}
}

func TestParseBytesRejectsJunk(t *testing.T) {
	t.Parallel()
	if _, err := ParseBytes(""); err == nil {
		t.Fatal("expected error")
	}
	if _, err := ParseBytes("nope"); err == nil {
		t.Fatal("expected error")
	}
}
