// Package proxy is the egress proxy sidecar: the only way out for agents. It enforces the egress
// profiles, injects credentials into requests so agents never hold them, and audits every connection.
package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// CA is the project's certificate authority. Its private key lives only in the proxy's private
// volume: agents get the certificate, never the key.
type CA struct {
	Cert    *x509.Certificate
	Key     *ecdsa.PrivateKey
	CertPEM []byte
}

const caValidity = 5 * 365 * 24 * time.Hour

// LoadOrCreateCA reads the CA from dir, creating it on first use. It persists across proxy
// recreation: regenerating it would force every agent to restart to pick up the new trust. A CA that is
// there but cannot be used (half present, unreadable, corrupt, a key that is not the certificate's) is an
// error and never replaced: replacing it silently would change what every agent trusts.
func LoadOrCreateCA(dir string) (*CA, error) {
	keyPath, certPath := filepath.Join(dir, "ca.key"), filepath.Join(dir, "ca.crt")
	keyPEM, keyErr := os.ReadFile(keyPath)
	certPEM, certErr := os.ReadFile(certPath)
	switch {
	case keyErr == nil && certErr == nil:
		return parseCA(keyPEM, certPEM)
	case errors.Is(keyErr, os.ErrNotExist) && errors.Is(certErr, os.ErrNotExist):
		ca, err := NewCA()
		if err != nil {
			return nil, err
		}
		return ca, ca.Save(dir)
	case keyErr != nil && !errors.Is(keyErr, os.ErrNotExist):
		return nil, keyErr
	case certErr != nil && !errors.Is(certErr, os.ErrNotExist):
		return nil, certErr
	}
	return nil, fmt.Errorf("the CA in %s is incomplete (ca.key: %v, ca.crt: %v): restore the missing file, or remove both to start a new CA", dir, keyErr == nil, certErr == nil)
}

// RotateCA replaces the CA in dir with a new one. Everything signed by the old one stops being
// trusted by agents once they have the new certificate, which is why agents are restarted after.
func RotateCA(dir string) (*CA, error) {
	ca, err := NewCA()
	if err != nil {
		return nil, err
	}
	return ca, ca.Save(dir)
}

// NewCA makes a CA in memory.
func NewCA() (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "egzo project CA", Organization: []string{"egzo"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return parseCA(keyPEM, certPEM)
}

// Save stores the CA in dir. Each file is written to a temporary name and renamed, so a crash leaves
// the old file or the new one, never half of one.
func (ca *CA) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(ca.Key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := writeAtomic(filepath.Join(dir, "ca.key"), keyPEM, 0o600); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "ca.crt"), ca.CertPEM, 0o644)
}

func parseCA(keyPEM, certPEM []byte) (*CA, error) {
	keyBlock, _ := pem.Decode(keyPEM)
	certBlock, _ := pem.Decode(certPEM)
	if keyBlock == nil || certBlock == nil {
		return nil, errors.New("CA files are not valid PEM")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("CA key: %w", err)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("CA certificate: %w", err)
	}
	if public, ok := cert.PublicKey.(*ecdsa.PublicKey); !ok || !public.Equal(&key.PublicKey) {
		return nil, errors.New("the CA key does not match the CA certificate")
	}
	return &CA{Cert: cert, Key: key, CertPEM: certPEM}, nil
}

// Fingerprint identifies the CA certificate.
func (ca *CA) Fingerprint() string {
	sum := sha256.Sum256(ca.Cert.Raw)
	return hex.EncodeToString(sum[:])
}

// Publish writes what agents need into the shared volume: the CA certificate, and a bundle of the
// system roots plus the CA, for tools that replace rather than extend their trust store.
func (ca *CA) Publish(dir, systemBundle string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, "ca.crt"), ca.CertPEM, 0o644); err != nil {
		return err
	}
	system, err := os.ReadFile(systemBundle)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	bundle := append([]byte{}, system...)
	if len(bundle) > 0 && bundle[len(bundle)-1] != '\n' {
		bundle = append(bundle, '\n')
	}
	bundle = append(bundle, ca.CertPEM...)
	return writeAtomic(filepath.Join(dir, "ca-bundle.crt"), bundle, 0o644)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}
