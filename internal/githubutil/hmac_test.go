package githubutil

import "testing"

func TestValidSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"action":"queued"}`)
	sig := SignBody("s3cret", body)
	if !ValidSignature("s3cret", sig, body) {
		t.Fatal("expected valid")
	}
	if ValidSignature("s3cret", "sha256=deadbeef", body) {
		t.Fatal("expected invalid")
	}
	if ValidSignature("", sig, body) {
		t.Fatal("empty secret must fail")
	}
}
