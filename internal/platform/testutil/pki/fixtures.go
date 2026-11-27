// Package pki creates isolated, short-lived certificates for integration tests.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Identity holds a certificate/key pair and its PEM file locations.
type Identity struct {
	CertFile, KeyFile string
	Pair              tls.Certificate
}

// Fixture separates server trust, client trust and an unrelated issuer.
type Fixture struct {
	Server, Core, Ingress, Unknown, Untrusted Identity
	ServerCAFile, ClientCAFile, OtherCAFile   string
	ServerRoots                               *x509.CertPool
}

type authority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	file string
}

// New generates fresh keys and certificates under t.TempDir.
func New(t *testing.T) Fixture {
	t.Helper()
	dir := t.TempDir()
	server := newCA(t, dir, "server-ca")
	client := newCA(t, dir, "client-ca")
	other := newCA(t, dir, "other-ca")
	roots := x509.NewCertPool()
	roots.AddCert(server.cert)
	return Fixture{
		Server:       issue(t, dir, "server", "fleet-manager", server, true),
		Core:         issue(t, dir, "core", "fleet-core", client, false),
		Ingress:      issue(t, dir, "ingress", "gateway-ingress", client, false),
		Unknown:      issue(t, dir, "unknown", "unknown-service", client, false),
		Untrusted:    issue(t, dir, "untrusted", "fleet-core", other, false),
		ServerCAFile: server.file, ClientCAFile: client.file, OtherCAFile: other.file, ServerRoots: roots,
	}
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func template(t *testing.T, cn string) *x509.Certificate {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	serial.Add(serial, big.NewInt(1))
	now := time.Now()
	return &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: cn}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true}
}

func newCA(t *testing.T, dir, name string) authority {
	t.Helper()
	key := newKey(t)
	cert := template(t, name)
	cert.IsCA = true
	cert.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	der := sign(t, cert, cert, key, key)
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, name+".crt")
	writePEM(t, file, "CERTIFICATE", der)
	return authority{cert: parsed, key: key, file: file}
}

func issue(t *testing.T, dir, name, cn string, ca authority, server bool) Identity {
	t.Helper()
	key := newKey(t)
	cert := template(t, cn)
	cert.KeyUsage = x509.KeyUsageDigitalSignature
	cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	if server {
		cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		cert.DNSNames = []string{"localhost"}
		cert.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	der := sign(t, cert, ca.cert, key, ca.key)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
	writePEM(t, certFile, "CERTIFICATE", der)
	writePEM(t, keyFile, "PRIVATE KEY", keyDER)
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	return Identity{CertFile: certFile, KeyFile: keyFile, Pair: pair}
}

func sign(t *testing.T, cert, parent *x509.Certificate, key, signer *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, cert, parent, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func writePEM(t *testing.T, path, kind string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
}

// Client returns an isolated TLS configuration. A supplied identity is forced
// onto the wire even when the server advertises a different issuer.
func (f Fixture) Client(identity *Identity) *tls.Config {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: f.ServerRoots}
	if identity != nil {
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &identity.Pair, nil }
	}
	return cfg
}
