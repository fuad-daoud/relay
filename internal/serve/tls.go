package serve

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// ErrTLSExists is returned when server.key or server.crt is already present.
var ErrTLSExists = errors.New("server key or certificate already exists")

// FingerprintOf returns "sha256:" + lower-case hex of sha256(der).
func FingerprintOf(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Fingerprint reads the certificate PEM file at certPath and returns its sha256 fingerprint.
func Fingerprint(certPath string) (string, error) {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", errors.New("no PEM certificate found in file")
	}
	return FingerprintOf(block.Bytes), nil
}

// InitTLS generates a server private key and self-signed certificate under dir.
// It writes server.key (0600) and server.crt (0644).
// If either file already exists, it returns ErrTLSExists without modifying either file.
func InitTLS(dir string, hosts []string, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	keyPath := filepath.Join(dir, "server.key")
	crtPath := filepath.Join(dir, "server.crt")

	if _, err := os.Stat(keyPath); err == nil {
		return "", ErrTLSExists
	}
	if _, err := os.Stat(crtPath); err == nil {
		return "", ErrTLSExists
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", err
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: keyDER,
	})

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return "", err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: "relay serve",
		},
		NotBefore:             now,
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	// SANs: hosts + localhost + 127.0.0.1 + machine hostname
	allHosts := append([]string(nil), hosts...)
	allHosts = append(allHosts, "localhost", "127.0.0.1")
	if hn, err := os.Hostname(); err == nil && hn != "" {
		allHosts = append(allHosts, hn)
	}

	dnsSeen := make(map[string]bool)
	ipSeen := make(map[string]bool)

	for _, h := range allHosts {
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			ipStr := ip.String()
			if !ipSeen[ipStr] {
				ipSeen[ipStr] = true
				template.IPAddresses = append(template.IPAddresses, ip)
			}
		} else {
			if !dnsSeen[h] {
				dnsSeen[h] = true
				template.DNSNames = append(template.DNSNames, h)
			}
		}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return "", err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(crtPath, certPEM, 0o644); err != nil {
		_ = os.Remove(keyPath)
		return "", err
	}

	return FingerprintOf(certDER), nil
}

// LoadTLS loads the server TLS certificate and private key from dir.
// The Leaf field is parsed and populated on the returned tls.Certificate.
func LoadTLS(dir string) (tls.Certificate, error) {
	crtPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")

	cert, err := tls.LoadX509KeyPair(crtPath, keyPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	if len(cert.Certificate) > 0 {
		cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return tls.Certificate{}, err
		}
	}
	return cert, nil
}
