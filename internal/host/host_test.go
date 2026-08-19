package host

import (
	"strings"
	"testing"
)

func TestCheckUbuntuAcceptsNoble(t *testing.T) {
	t.Parallel()
	info, err := ParseOSRelease(strings.NewReader(`
ID=ubuntu
VERSION_ID="24.04"
PRETTY_NAME="Ubuntu 24.04.4 LTS"
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckUbuntu(info); err != nil {
		t.Fatal(err)
	}
}

func TestCheckUbuntuRejectsDebian(t *testing.T) {
	t.Parallel()
	info := Info{ID: "debian", VersionID: "12", Pretty: "Debian GNU/Linux 12"}
	err := CheckUbuntu(info)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "only runs on Ubuntu") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckUbuntuRejectsOldUbuntu(t *testing.T) {
	t.Parallel()
	info := Info{ID: "ubuntu", VersionID: "22.04", Pretty: "Ubuntu 22.04.5 LTS"}
	err := CheckUbuntu(info)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "24.04") {
		t.Fatalf("unexpected error: %v", err)
	}
}
