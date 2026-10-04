package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"sync"
	"time"
)

const leafValidity = 24 * time.Hour

// Minter signs short-lived certificates for the hosts the proxy intercepts and caches them in memory.
type Minter struct {
	ca    *CA
	mu    sync.Mutex
	cache map[string]*tls.Certificate
	now   func() time.Time
}

func NewMinter(ca *CA) *Minter {
	return &Minter{ca: ca, cache: map[string]*tls.Certificate{}, now: time.Now}
}

// Certificate returns a certificate for host, valid for at least another hour.
func (m *Minter) Certificate(host string) (*tls.Certificate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cached, ok := m.cache[host]; ok && cached.Leaf.NotAfter.After(m.now().Add(time.Hour)) {
		return cached, nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    m.now().Add(-5 * time.Minute),
		NotAfter:     m.now().Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, m.ca.Cert, &key.PublicKey, m.ca.Key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	certificate := &tls.Certificate{Certificate: [][]byte{der, m.ca.Cert.Raw}, PrivateKey: key, Leaf: leaf}
	m.cache[host] = certificate
	return certificate, nil
}
