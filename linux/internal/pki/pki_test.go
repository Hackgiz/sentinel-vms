package pki

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCAIsCreatedOnceAndReloaded(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")
	ca, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ca.Cert.IsCA || ca.Cert.NotAfter.Before(time.Now().Add(9*365*24*time.Hour)) {
		t.Fatalf("CA cert: isCA %v until %v", ca.Cert.IsCA, ca.Cert.NotAfter)
	}
	if fi, err := os.Stat(filepath.Join(dir, "ca.key")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}
	again, err := LoadOrCreateCA(dir)
	if err != nil || again.Fingerprint() != ca.Fingerprint() || len(ca.Fingerprint()) != 64 {
		t.Fatalf("reload: %v %s vs %s", err, again.Fingerprint(), ca.Fingerprint())
	}
	os.Remove(filepath.Join(dir, "ca.key"))
	if _, err := LoadOrCreateCA(dir); err == nil {
		t.Fatal("a CA without its key must not be replaced silently")
	}
}

func TestServerAndRecorderCertificates(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := ca.IssueServer([]string{"ms.lan", "10.1.2.3"}, CertLifetime)
	if err != nil {
		t.Fatal(err)
	}
	if len(srv.Certificate) != 2 || !bytes.Equal(srv.Certificate[1], ca.Cert.Raw) {
		t.Fatal("server chain must carry the CA")
	}
	for _, host := range []string{"ms.lan", "10.1.2.3"} {
		if _, err := srv.Leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: ca.Pool()}); err != nil {
			t.Errorf("verify %s: %v", host, err)
		}
	}
	if _, err := srv.Leaf.Verify(x509.VerifyOptions{DNSName: "other.lan", Roots: ca.Pool()}); err == nil {
		t.Error("server cert must not cover other names")
	}

	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	csr, err := CreateCSR(key, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.SignCSR(csr, "server-1", CertLifetime)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "server-1" || len(cert.DNSNames) != 0 || len(cert.IPAddresses) != 0 {
		t.Fatalf("subject %v names %v %v", cert.Subject, cert.DNSNames, cert.IPAddresses)
	}
	if d := time.Until(cert.NotAfter); d < 364*24*time.Hour || d > 366*24*time.Hour {
		t.Fatalf("lifetime %v", d)
	}
	for _, eku := range []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth} {
		if _, err := cert.Verify(x509.VerifyOptions{Roots: ca.Pool(), KeyUsages: []x509.ExtKeyUsage{eku}}); err != nil {
			t.Errorf("eku %v: %v", eku, err)
		}
	}
	// A recorder certificate is not good for the management server's names.
	if _, err := cert.Verify(x509.VerifyOptions{DNSName: "ms.lan", Roots: ca.Pool()}); err == nil {
		t.Error("recorder cert must not pass as the management server")
	}
	if Fingerprint(cert.Raw) == Fingerprint(srv.Leaf.Raw) {
		t.Fatal("fingerprints collide")
	}
	// The key works with the certificate for TLS.
	keyPEM, _ := EncodeKeyPEM(key)
	if _, err := tls.X509KeyPair(CertPEM(cert.Raw), keyPEM); err != nil {
		t.Fatal(err)
	}
	if k2, err := ParseKeyPEM(keyPEM); err != nil || !k2.Equal(key) {
		t.Fatal("key round trip", err)
	}

	if _, err := ca.SignCSR([]byte("junk"), "x", CertLifetime); err == nil {
		t.Error("junk CSR accepted")
	}
	tampered := bytes.Replace(csr, []byte("A"), []byte("B"), 1)
	if _, err := ca.SignCSR(tampered, "x", CertLifetime); err == nil {
		t.Error("tampered CSR accepted")
	}
}

func TestLocalHosts(t *testing.T) {
	hosts := LocalHosts()
	has := func(h string) bool {
		for _, x := range hosts {
			if x == h {
				return true
			}
		}
		return false
	}
	if !has("localhost") || !has("127.0.0.1") {
		t.Fatalf("hosts %v", hosts)
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil && ip.IsLinkLocalUnicast() {
			t.Errorf("link-local %s", h)
		}
	}
}

func TestSeal(t *testing.T) {
	k, err := NewSealKey()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ParseSealPublicKey(k.PublicString())
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := Seal(pub, []byte("p@ss"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "p@ss") {
		t.Fatal("not sealed")
	}
	again, err := ParseSealKey(k.PrivateString())
	if err != nil || again.Public != k.Public {
		t.Fatalf("seal key round trip: %v", err)
	}
	got, err := again.Open(sealed)
	if err != nil || string(got) != "p@ss" {
		t.Fatalf("open: %q %v", got, err)
	}
	other, _ := NewSealKey()
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("another key opened it")
	}
	if _, err := k.Open("!!"); err == nil {
		t.Fatal("garbage opened")
	}
}

func TestEnrollmentToken(t *testing.T) {
	ca, _ := NewCA()
	tok := FormatToken("s3cr-et_X", ca.Pin())
	if !strings.HasPrefix(tok, "sen1.s3cr-et_X.") {
		t.Fatal(tok)
	}
	secret, pin, err := ParseToken(" " + tok + "\n")
	if err != nil || secret != "s3cr-et_X" || !MatchesPin(ca.Cert, pin) {
		t.Fatalf("parse: %q %v", secret, err)
	}
	other, _ := NewCA()
	if MatchesPin(other.Cert, pin) {
		t.Fatal("pin matches another CA")
	}
	for _, bad := range []string{"", "sen1", "sen2.a." + strings.Split(tok, ".")[2], "sen1..x", "sen1.a.b.c", "sen1.a.short", "sen1.a.!!!"} {
		if _, _, err := ParseToken(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}
