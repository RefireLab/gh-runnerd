package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"testing"
)

func TestGenerateServesExpectedSANs(t *testing.T) {
	t.Parallel()
	b, err := Generate(DefaultSANs(), []net.IP{net.ParseIP("10.87.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b.ServerCertPEM)
	if block == nil {
		t.Fatal("no server cert pem")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.VerifyHostname("gh-runnerd.local"); err != nil {
		t.Fatal(err)
	}
	if err := cert.VerifyHostname("ghcr.gh-runnerd.local"); err != nil {
		t.Fatal(err)
	}
	if err := cert.VerifyHostname("10.87.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := tls.X509KeyPair(b.ServerCertPEM, b.ServerKeyPEM); err != nil {
		t.Fatal(err)
	}
}

func TestWriteLoadRoundTrip(t *testing.T) {
	t.Parallel()
	b, err := Generate(DefaultSANs(), nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := b.Write(dir); err != nil {
		t.Fatal(err)
	}
	if !Exists(dir) {
		t.Fatal("expected bundle on disk")
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.CACertPEM) != string(b.CACertPEM) {
		t.Fatal("ca cert mismatch")
	}
}
