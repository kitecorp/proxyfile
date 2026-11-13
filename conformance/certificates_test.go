package conformance

import (
	"crypto/rand"
	"crypto/rsa"
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

// tlsFiles are what certificates makes. tls and jwt are folders mounted at /etc/gateway/tls and /etc/gateway/jwt, the
// paths the full example's tls keys and auth.jwt.ca name. client and otherCA are the clients' certificates with their
// keys, kept in memory because the proxy never reads them: client is signed by the clients CA, and otherCA by a
// second CA that carries the clients CA's name, so only a proxy that checks the signature refuses it.
type tlsFiles struct {
	tls, jwt        string
	client, otherCA tls.Certificate
}

// certificates makes throwaway TLS files for one run under dir: a self-signed server certificate for localhost and
// 127.0.0.1 with its key, a clients CA, and a copy of the server certificate as the CA bundle auth.jwt.ca names.
// Folders are 0755 and files 0644 whatever the umask, because the proxy runs as an unprivileged user in its image and
// must read the mounted files; the keys are made for this run alone and removed with dir. It also makes the two client
// certificates, which stay in memory.
func certificates(t testing.TB, dir string) tlsFiles {
	files := tlsFiles{tls: filepath.Join(dir, "tls"), jwt: filepath.Join(dir, "jwt")}
	for _, folder := range []string{files.tls, files.jwt} {
		if err := os.Mkdir(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(folder, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The server certificate is its own issuer and a CA, as openssl req -x509 makes it, so the copy in jwt/ca.pem is
	// a CA bundle the proxy loads as it loads the one scripts/validate-envoy.sh makes.
	serverKey := newKey(t)
	server := &x509.Certificate{
		Subject:               pkix.Name{CommonName: "localhost"},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	_, serverPEM := sign(t, server, server, serverKey, serverKey)
	caCertificate, caKey, caPEM := clientsCA(t)
	files.client = clientCertificate(t, caCertificate, caKey)
	otherCertificate, otherKey, _ := clientsCA(t)
	files.otherCA = clientCertificate(t, otherCertificate, otherKey)
	for path, data := range map[string][]byte{
		filepath.Join(files.tls, "certificate.pem"): serverPEM,
		filepath.Join(files.tls, "key.pem"):         keyPEM(t, serverKey),
		filepath.Join(files.tls, "clients-ca.pem"):  caPEM,
		filepath.Join(files.jwt, "ca.pem"):          serverPEM,
	} {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

// clientsCA makes a CA named clients-ca. Each call makes a new key, so two calls make two CAs of one name.
func clientsCA(t testing.TB) (*x509.Certificate, *rsa.PrivateKey, []byte) {
	key := newKey(t)
	ca := &x509.Certificate{
		Subject:               pkix.Name{CommonName: "clients-ca"},
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	certificate, certificatePEM := sign(t, ca, ca, key, key)
	return certificate, key, certificatePEM
}

// clientCertificate makes a client certificate ca signs, with its key, as a TLS client presents it. The proxy checks
// the certificate's purpose, so it carries the extended key usage client auth.
func clientCertificate(t testing.TB, ca *x509.Certificate, caKey *rsa.PrivateKey) tls.Certificate {
	key := newKey(t)
	client := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "client"},
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	certificate, _ := sign(t, client, ca, key, caKey)
	return tls.Certificate{Certificate: [][]byte{certificate.Raw}, PrivateKey: key}
}

// newKey makes an RSA 2048 key, the kind the openssl commands of scripts/validate-envoy.sh make.
func newKey(t testing.TB) *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// sign fills in the serial number and validity of template, has parentKey sign it and returns the certificate, both
// parsed and as PEM. The parsed one carries the subject key identifier CreateCertificate adds to a CA, so a
// certificate it signs names its issuer's key. The validity starts an hour back, so a clock in the container's
// virtual machine a little behind the host's still accepts it.
func sign(t testing.TB, template, parent *x509.Certificate, key, parentKey *rsa.PrivateKey) (*x509.Certificate, []byte) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		t.Fatal(err)
	}
	template.SerialNumber = serial
	template.NotBefore = time.Now().Add(-time.Hour)
	template.NotAfter = time.Now().Add(24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// keyPEM writes key as PKCS #8, the form openssl writes.
func keyPEM(t testing.TB, key *rsa.PrivateKey) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}
