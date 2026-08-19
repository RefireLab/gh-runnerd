package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Bundle is a CA plus a server certificate issued from it.
type Bundle struct {
	CACertPEM     []byte
	CAKeyPEM      []byte
	ServerCertPEM []byte
	ServerKeyPEM  []byte
}

// Generate creates a 10-year internal CA and a server cert for the given hosts/IPs.
func Generate(hosts []string, ips []net.IP) (Bundle, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Bundle{}, err
	}
	caTpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "gh-runnerd internal CA", Organization: []string{"gh-runnerd"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		return Bundle{}, err
	}

	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Bundle{}, err
	}
	srvTpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "gh-runnerd.local", Organization: []string{"gh-runnerd"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     hosts,
		IPAddresses:  ips,
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTpl, caTpl, &srvKey.PublicKey, caKey)
	if err != nil {
		return Bundle{}, err
	}

	caKeyPEM, err := marshalECKey(caKey)
	if err != nil {
		return Bundle{}, err
	}
	srvKeyPEM, err := marshalECKey(srvKey)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{
		CACertPEM:     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		CAKeyPEM:      caKeyPEM,
		ServerCertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srvDER}),
		ServerKeyPEM:  srvKeyPEM,
	}, nil
}

func marshalECKey(key *ecdsa.PrivateKey) ([]byte, error) {
	b, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: b}), nil
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return big.NewInt(time.Now().UnixNano())
	}
	return n
}

// DefaultSANs are the DNS names baked into the VM trust store.
func DefaultSANs() []string {
	return []string{
		"gh-runnerd.local",
		"dockerhub.gh-runnerd.local",
		"ghcr.gh-runnerd.local",
		"quay.gh-runnerd.local",
	}
}

// Write stores the bundle under dir as ca.crt, ca.key, server.crt, server.key.
func (b Bundle) Write(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	files := map[string][]byte{
		"ca.crt":     b.CACertPEM,
		"ca.key":     b.CAKeyPEM,
		"server.crt": b.ServerCertPEM,
		"server.key": b.ServerKeyPEM,
	}
	for name, body := range files {
		mode := os.FileMode(0o600)
		if name == "ca.crt" || name == "server.crt" {
			mode = 0o644
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, mode); err != nil {
			return err
		}
	}
	return nil
}

// Load reads a previously written bundle.
func Load(dir string) (Bundle, error) {
	read := func(name string) ([]byte, error) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		return b, nil
	}
	var (
		b   Bundle
		err error
	)
	if b.CACertPEM, err = read("ca.crt"); err != nil {
		return Bundle{}, err
	}
	if b.CAKeyPEM, err = read("ca.key"); err != nil {
		return Bundle{}, err
	}
	if b.ServerCertPEM, err = read("server.crt"); err != nil {
		return Bundle{}, err
	}
	if b.ServerKeyPEM, err = read("server.key"); err != nil {
		return Bundle{}, err
	}
	return b, nil
}

// Exists reports whether a complete bundle is on disk.
func Exists(dir string) bool {
	for _, name := range []string{"ca.crt", "ca.key", "server.crt", "server.key"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}
