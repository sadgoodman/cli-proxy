// Package ca generates and caches the certificate authority used to
// man-in-the-middle TLS connections, plus short-lived leaf certificates for
// every intercepted host.
//
// It depends only on the Go standard library so the whole tool stays a single
// static binary with no third-party modules.
package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	certFileName = "ca.pem"
	keyFileName  = "ca-key.pem"

	// Leaf certificates are deliberately kept well under the 398 day limit
	// Apple enforces on TLS server certificates so that iOS and macOS accept
	// them without extra trust settings.
	leafValidity = 365 * 24 * time.Hour
	caValidity   = 10 * 365 * 24 * time.Hour
)

// CA is a certificate authority plus a cache of minted leaf certificates.
type CA struct {
	Cert    *x509.Certificate
	PEM     []byte
	KeyPEM  []byte
	CertPEM []byte

	// Dir is where the CA material lives on disk.
	Dir string

	key *rsa.PrivateKey

	mu    sync.Mutex
	cache map[string]*tls.Certificate
}

// Dir returns the default on-disk location for CA material.
func Dir() string {
	if v := os.Getenv("CLI_PROXY_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".cli-proxy"
	}
	return filepath.Join(home, ".cli-proxy")
}

// Load reads the CA from dir, generating a fresh one when it is missing or
// unreadable.
func Load(dir string) (*CA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create ca dir: %w", err)
	}
	certPath := filepath.Join(dir, certFileName)
	keyPath := filepath.Join(dir, keyFileName)

	if c, err := loadFrom(certPath, keyPath); err == nil {
		c.Dir = dir
		return c, nil
	}
	c, err := generate(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	c.Dir = dir
	return c, nil
}

func loadFrom(certPath, keyPath string) (*CA, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, errors.New("ca: malformed certificate PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, err
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, errors.New("ca: malformed key PEM")
	}
	key, err := parseRSAKey(keyBlock.Bytes)
	if err != nil {
		return nil, err
	}
	if time.Now().After(cert.NotAfter.Add(-24 * time.Hour)) {
		return nil, errors.New("ca: certificate expired")
	}
	return &CA{
		Cert:    cert,
		PEM:     certPEM,
		KeyPEM:  keyPEM,
		CertPEM: certPEM,
		key:     key,
		cache:   make(map[string]*tls.Certificate),
	}, nil
}

func parseRSAKey(der []byte) (*rsa.PrivateKey, error) {
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("ca: key is not RSA")
	}
	return rk, nil
}

func generate(certPath, keyPath string) (*CA, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate ca key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "cli-proxy Root CA",
			Organization: []string{"cli-proxy"},
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create ca cert: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	return &CA{
		Cert:    cert,
		PEM:     certPEM,
		KeyPEM:  keyPEM,
		CertPEM: certPEM,
		key:     key,
		cache:   make(map[string]*tls.Certificate),
	}, nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, err
	}
	return n, nil
}

// Leaf returns (minting if needed) a certificate valid for host. host may
// include a port, which is stripped.
func (c *CA) Leaf(host string) (*tls.Certificate, error) {
	name := normalizeHost(host)
	if name == "" {
		return nil, errors.New("ca: empty host")
	}

	c.mu.Lock()
	if cert, ok := c.cache[name]; ok {
		c.mu.Unlock()
		return cert, nil
	}
	c.mu.Unlock()

	cert, err := c.mint(name)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.cache[name] = cert
	// Keep the cache from growing without bound on long sessions.
	if len(c.cache) > 2048 {
		for k := range c.cache {
			if k != name {
				delete(c.cache, k)
				break
			}
		}
	}
	c.mu.Unlock()
	return cert, nil
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	return strings.ToLower(host)
}

func (c *CA) mint(name string) (*tls.Certificate, error) {
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   name,
			Organization: []string{"cli-proxy"},
		},
		NotBefore:   now.Add(-time.Hour),
		NotAfter:    now.Add(leafValidity),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{name},
	}
	if ip := net.ParseIP(name); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
		tmpl.DNSNames = nil
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.Cert, &leafKey.PublicKey, c.key)
	if err != nil {
		return nil, fmt.Errorf("sign leaf for %s: %w", name, err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{
		Certificate: [][]byte{der, c.Cert.Raw},
		PrivateKey:  leafKey,
		Leaf:        leaf,
	}, nil
}

// TLSCertificate implements the lighter alternative to Leaf for callers that
// only need the raw tls.Certificate.
func (c *CA) TLSCertificate(host string) (*tls.Certificate, error) { return c.Leaf(host) }

// CertPath is the on-disk PEM file for the root certificate. External tools
// such as the platform trust stores need a real path.
func (c *CA) CertPath() string {
	if c.Dir == "" {
		return ""
	}
	return filepath.Join(c.Dir, certFileName)
}

// CommonName is the subject the trust stores will show.
func (c *CA) CommonName() string { return c.Cert.Subject.CommonName }

// Fingerprint returns the SHA-256 fingerprint of the CA certificate in the
// colon-separated hex form browsers display.
func (c *CA) Fingerprint() string {
	sum := sha256.Sum256(c.Cert.Raw)
	hexed := strings.ToUpper(hex.EncodeToString(sum[:]))
	parts := make([]string, 0, len(hexed)/2)
	for i := 0; i+1 < len(hexed); i += 2 {
		parts = append(parts, hexed[i:i+2])
	}
	return strings.Join(parts, ":")
}

// Summary returns a one line human readable description of the CA.
func (c *CA) Summary() string {
	return fmt.Sprintf("%s (expires %s)", c.Cert.Subject.CommonName, c.Cert.NotAfter.Format("2006-01-02"))
}
