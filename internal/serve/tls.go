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
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// ErrTLSExists is returned when the server key or certificate secret is
// already set.
var ErrTLSExists = errors.New("server key or certificate already exists")

// The machine database's secret names holding the server TLS material (P5
// §3), replacing <serveRoot>/server.key and server.crt.
const (
	tlsKeySecret  = "serve.tls.key"
	tlsCertSecret = "serve.tls.cert"
)

// SecretStore is the secret surface the TLS helpers read and write (P5 §4.5):
// the machine database's secret table, plus the legacy serve root whose
// server.key and server.crt are imported into the secrets and removed on first
// use.
type SecretStore struct {
	DB   *db.DB
	Root string
}

// SecretGet reads one secret. A nil database reports every secret absent.
func (s SecretStore) SecretGet(name string) ([]byte, bool, error) {
	if s.DB == nil {
		return nil, false, nil
	}
	return s.DB.SecretGet(name)
}

// SecretPut writes one secret.
func (s SecretStore) SecretPut(name string, value []byte, now time.Time) error {
	return s.DB.Tx(func(t *db.Tx) error { return t.SecretPut(name, value, now) })
}

// importLegacy adopts the legacy server.key and server.crt files into the
// secrets and removes each file it imported (P5 §4.5): a secret that is
// already set wins, and its file is left where it is. The write happens before
// the delete, so a crash between them re-imports on the next read rather than
// losing the material.
func (s SecretStore) importLegacy() error {
	if s.Root == "" || s.DB == nil {
		return nil
	}
	keyPath := filepath.Join(s.Root, "server.key")
	crtPath := filepath.Join(s.Root, "server.crt")

	var importedKey, importedCert bool

	if _, ok, err := s.DB.SecretGet(tlsKeySecret); err != nil {
		return err
	} else if !ok {
		data, rerr := os.ReadFile(keyPath)
		switch {
		case rerr == nil:
			if err := s.SecretPut(tlsKeySecret, data, time.Now().UTC()); err != nil {
				return err
			}
			importedKey = true
		case errors.Is(rerr, os.ErrNotExist):
		default:
			return rerr
		}
	}

	if _, ok, err := s.DB.SecretGet(tlsCertSecret); err != nil {
		return err
	} else if !ok {
		data, rerr := os.ReadFile(crtPath)
		switch {
		case rerr == nil:
			if err := s.SecretPut(tlsCertSecret, data, time.Now().UTC()); err != nil {
				return err
			}
			importedCert = true
		case errors.Is(rerr, os.ErrNotExist):
		default:
			return rerr
		}
	}

	// Both writes are committed: only now are the imported files removed.
	if importedKey {
		if rerr := os.Remove(keyPath); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			slog.Warn("tls import: could not remove imported key file", "path", keyPath, "err", rerr)
		}
	}
	if importedCert {
		if rerr := os.Remove(crtPath); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			slog.Warn("tls import: could not remove imported certificate file", "path", crtPath, "err", rerr)
		}
	}
	return nil
}

// FingerprintOf returns "sha256:" + lower-case hex of sha256(der).
func FingerprintOf(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Fingerprint reads the certificate secret and returns its sha256 fingerprint.
func Fingerprint(secrets SecretStore) (string, error) {
	if err := secrets.importLegacy(); err != nil {
		return "", err
	}
	data, ok, err := secrets.SecretGet(tlsCertSecret)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("no server certificate")
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", errors.New("no PEM certificate found")
	}
	return FingerprintOf(block.Bytes), nil
}

// InitTLS generates a server private key and self-signed certificate into the
// secrets serve.tls.key and serve.tls.cert (P5 §4.5).
// If either secret is already set, it returns ErrTLSExists without modifying either.
func InitTLS(secrets SecretStore, hosts []string, now time.Time) (string, error) {
	if err := secrets.importLegacy(); err != nil {
		return "", err
	}

	if _, ok, err := secrets.SecretGet(tlsKeySecret); err != nil {
		return "", err
	} else if ok {
		return "", ErrTLSExists
	}
	if _, ok, err := secrets.SecretGet(tlsCertSecret); err != nil {
		return "", err
	} else if ok {
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
			CommonName: "relevo serve",
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

	if err := secrets.SecretPut(tlsKeySecret, keyPEM, now); err != nil {
		return "", err
	}
	if err := secrets.SecretPut(tlsCertSecret, certPEM, now); err != nil {
		_ = secrets.DB.Tx(func(t *db.Tx) error { return t.SecretDelete(tlsKeySecret) })
		return "", err
	}

	return FingerprintOf(certDER), nil
}

// LoadTLS loads the server TLS certificate and private key from the secrets.
// The Leaf field is parsed and populated on the returned tls.Certificate.
func LoadTLS(secrets SecretStore) (tls.Certificate, error) {
	if err := secrets.importLegacy(); err != nil {
		return tls.Certificate{}, err
	}
	certPEM, ok, err := secrets.SecretGet(tlsCertSecret)
	if err != nil {
		return tls.Certificate{}, err
	}
	if !ok {
		return tls.Certificate{}, errors.New("no server certificate")
	}
	keyPEM, ok, err := secrets.SecretGet(tlsKeySecret)
	if err != nil {
		return tls.Certificate{}, err
	}
	if !ok {
		return tls.Certificate{}, errors.New("no server key")
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
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
