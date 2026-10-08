// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

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
	ca       *CA
	mu       sync.Mutex
	cache    map[string]*tls.Certificate
	order    []string // host names, oldest first
	maxCache int
	now      func() time.Time
}

// defaultMaxCache bounds the cache: with a wildcard service the host names are the agent's to choose.
const defaultMaxCache = 1024

func NewMinter(ca *CA) *Minter {
	return &Minter{ca: ca, cache: map[string]*tls.Certificate{}, maxCache: defaultMaxCache, now: time.Now}
}

func (m *Minter) size() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.cache)
}

// remember stores a certificate, first making room: expired ones go, then the oldest. It runs under m.mu.
func (m *Minter) remember(host string, certificate *tls.Certificate) {
	if _, present := m.cache[host]; !present && len(m.cache) >= m.maxCache {
		kept := m.order[:0]
		for _, name := range m.order {
			if cached, ok := m.cache[name]; ok && cached.Leaf.NotAfter.After(m.now().Add(time.Hour)) {
				kept = append(kept, name)
			} else {
				delete(m.cache, name)
			}
		}
		m.order = kept
		for len(m.cache) >= m.maxCache && len(m.order) > 0 {
			delete(m.cache, m.order[0])
			m.order = m.order[1:]
		}
	}
	if _, present := m.cache[host]; !present {
		m.order = append(m.order, host)
	}
	m.cache[host] = certificate
}

// SetCA switches to a new CA: the certificates minted by the old one are forgotten.
func (m *Minter) SetCA(ca *CA) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ca = ca
	m.cache = map[string]*tls.Certificate{}
	m.order = nil
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
	m.remember(host, certificate)
	return certificate, nil
}
