// Package pki is the control link's key material (PLATFORM.md, "Control
// link wire protocol"):
//
//   - the management server's internal CA (Ed25519, <data>/ca/, 10 years),
//     which issues the control listener's server certificate and every
//     recording server's certificate (1 year, client + server EKU);
//   - certificate requests and fingerprints for the recording side;
//   - X25519 sealing of camera credentials to one recording server
//     (nacl/box anonymous);
//   - the enrollment token format, sen1.<secret>.<CA pin>.
//
// It depends only on the standard library and x/crypto, so both sides use it.
package pki

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
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
	"time"
)

const (
	CALifetime     = 10 * 365 * 24 * time.Hour
	CertLifetime   = 365 * 24 * time.Hour
	backdate       = 5 * time.Minute // tolerate a peer's clock being a little behind
	caCertFile     = "ca.crt"
	caKeyFile      = "ca.key"
	caCommonName   = "Sentinel Management Server CA"
	serverCertName = "Sentinel Management Server"
)

// CA is the management server's internal certificate authority.
type CA struct {
	Cert *x509.Certificate
	Key  ed25519.PrivateKey
}

// LoadOrCreateCA reads the CA from dir (<data>/ca), creating it on first
// start.
func LoadOrCreateCA(dir string) (*CA, error) {
	certPEM, err1 := os.ReadFile(filepath.Join(dir, caCertFile))
	keyPEM, err2 := os.ReadFile(filepath.Join(dir, caKeyFile))
	if err1 == nil && err2 == nil {
		cert, err := ParseCertPEM(certPEM)
		if err != nil {
			return nil, fmt.Errorf("pki: %s: %w", caCertFile, err)
		}
		key, err := ParseKeyPEM(keyPEM)
		if err != nil {
			return nil, fmt.Errorf("pki: %s: %w", caKeyFile, err)
		}
		if !key.Public().(ed25519.PublicKey).Equal(cert.PublicKey) {
			return nil, errors.New("pki: CA key and certificate don't match")
		}
		return &CA{Cert: cert, Key: key}, nil
	}
	if !errors.Is(err1, os.ErrNotExist) || !errors.Is(err2, os.ErrNotExist) {
		if err1 == nil {
			err1 = err2
		}
		return nil, fmt.Errorf("pki: CA is incomplete in %s: %w", dir, err1)
	}
	ca, err := NewCA()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	keyPEM, err = EncodeKeyPEM(ca.Key)
	if err != nil {
		return nil, err
	}
	if err := WriteFileAtomic(filepath.Join(dir, caKeyFile), keyPEM, 0o600); err != nil {
		return nil, err
	}
	if err := WriteFileAtomic(filepath.Join(dir, caCertFile), ca.PEM(), 0o644); err != nil {
		return nil, err
	}
	return ca, nil
}

// NewCA makes a new CA in memory.
func NewCA() (*CA, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: caCommonName, Organization: []string{"Sentinel"}},
		NotBefore:             now.Add(-backdate),
		NotAfter:              now.Add(CALifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{Cert: cert, Key: key}, nil
}

// PEM is the CA certificate as PEM (what recording servers pin).
func (ca *CA) PEM() []byte { return CertPEM(ca.Cert.Raw) }

// Fingerprint is the SHA-256 of the CA certificate DER, hex.
func (ca *CA) Fingerprint() string { return Fingerprint(ca.Cert.Raw) }

// Pin is the raw SHA-256 of the CA certificate DER (the enrollment token's
// last part).
func (ca *CA) Pin() []byte {
	sum := sha256.Sum256(ca.Cert.Raw)
	return sum[:]
}

// Pool is a cert pool holding only this CA.
func (ca *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.Cert)
	return p
}

// IssueServer issues the control listener's TLS certificate for hosts
// (DNS names and IP addresses), chained with the CA so a recording server
// can find the pinned CA during enrollment.
func (ca *CA) IssueServer(hosts []string, ttl time.Duration) (tls.Certificate, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: serverCertName, Organization: []string{"Sentinel"}},
		NotBefore:    now.Add(-backdate),
		NotAfter:     ca.clamp(now.Add(ttl)),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, key.Public(), ca.Key)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der, ca.Cert.Raw}, PrivateKey: key, Leaf: leaf}, nil
}

// SignCSR issues a recording server certificate (client + server EKU) for
// the CSR's key, with commonName as its subject (the server id); the CSR's
// own subject and names are ignored. It returns the certificate.
func (ca *CA) SignCSR(csrPEM []byte, commonName string, ttl time.Duration) (*x509.Certificate, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("pki: not a PEM certificate request")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("pki: certificate request signature: %w", err)
	}
	if _, ok := csr.PublicKey.(ed25519.PublicKey); !ok {
		return nil, errors.New("pki: certificate request must use an Ed25519 key")
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: commonName, Organization: []string{"Sentinel Recording Server"}},
		NotBefore:    now.Add(-backdate),
		NotAfter:     ca.clamp(now.Add(ttl)),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, csr.PublicKey, ca.Key)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

// clamp keeps a certificate inside the CA's validity.
func (ca *CA) clamp(t time.Time) time.Time {
	if t.After(ca.Cert.NotAfter) {
		return ca.Cert.NotAfter
	}
	return t
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err) // crypto/rand doesn't fail
	}
	return n
}

// MARK: - Recording side

// NewKey makes an Ed25519 TLS key.
func NewKey() (ed25519.PrivateKey, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	return key, err
}

// CreateCSR makes a PEM certificate request for key. The CA sets the
// subject itself; commonName is informational.
func CreateCSR(key crypto.Signer, commonName string) ([]byte, error) {
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: commonName}}, key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// MARK: - Encoding

// Fingerprint is the SHA-256 of a certificate's DER, lowercase hex.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// CertPEM encodes a certificate's DER.
func CertPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// ParseCertPEM decodes the first certificate in data.
func ParseCertPEM(data []byte) (*x509.Certificate, error) {
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return nil, errors.New("pki: no PEM certificate")
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
	}
}

// EncodeKeyPEM encodes an Ed25519 private key (PKCS #8).
func EncodeKeyPEM(key ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// ParseKeyPEM decodes an Ed25519 private key (PKCS #8).
func ParseKeyPEM(data []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("pki: no PEM private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("pki: private key is not Ed25519")
	}
	return key, nil
}

// WriteFileAtomic writes data to a temporary file next to path and renames
// it over path, so a crash never leaves half a key or certificate.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after the rename
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// MARK: - Host names

// LocalHosts are the names this machine answers to: its hostname (and
// hostname.local), localhost, and the addresses of its up interfaces.
// The control listener's certificate covers them plus --control-hosts.
func LocalHosts() []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		h = strings.TrimSpace(strings.TrimSuffix(h, "."))
		if h != "" && !seen[strings.ToLower(h)] {
			seen[strings.ToLower(h)] = true
			out = append(out, h)
		}
	}
	add("localhost")
	if hn, err := os.Hostname(); err == nil {
		add(hn)
		if !strings.Contains(hn, ".") {
			add(hn + ".local")
		}
	}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLinkLocalUnicast() {
				add(ipn.IP.String())
			}
		}
	}
	add("127.0.0.1")
	add("::1")
	return out
}
