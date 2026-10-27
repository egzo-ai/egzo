// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) Neopeak Internet Solutions inc.

package proxy

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/egzo-ai/egzo/internal/seal"
)

// The files of the private volume. The private halves are sealed with keys derived from the project's secret key; the
// public halves are in clear, so that a restarted proxy can publish them before it is given the key. ca.key and the
// clear ssh host key are what a proxy kept before the secret key: a volume that holds one is refused.
const (
	sshHostKeyFile     = "ssh_host_ed25519_key"
	caCertFile         = "ca.crt"
	caKeyFile          = "ca.key"
	caSealedFile       = "ca.key.sealed"
	sshHostSealedFile  = sshHostKeyFile + ".sealed"
	sshHostPublicFile  = sshHostKeyFile + ".pub"
	wrongSecretKeyHelp = "give the secret key it was sealed with back, or run `egzo project rotate-ca` for a new CA (the agents restart)"
)

// ErrClearStore says the private volume is from an egzo before the secret key, which kept the private keys in clear.
// Before 1.0 egzo does not read what an older one made: the project is made again.
var ErrClearStore = errors.New("the proxy's volume holds a CA key in clear, made by an egzo from before the secret key, which this one does not read: " +
	"run `egzo project down`, then `egzo project up` (a new CA; the workspaces are kept)")

// ErrWrongSecretKey says the private volume was sealed with another secret key (or for another project).
var ErrWrongSecretKey = errors.New("the CA in the proxy's volume was sealed with another secret key: " + wrongSecretKeyHelp)

// Store is what the proxy keeps in its private volume: the CA and the ssh host key. Until a policy brings the secret key,
// only their public halves are known.
type Store struct {
	dir string

	mu      sync.Mutex
	keys    *seal.Keys // the keys that opened the store; nil until then
	ca      *CA        // nil when there is none yet; without Key while sealed
	hostKey ssh.Signer // nil until opened
	hostPub ssh.PublicKey
}

// OpenStore reads what can be read of dir without the secret key. A volume from before the secret key is ErrClearStore.
func OpenStore(dir string) (*Store, error) {
	s := &Store{dir: dir}
	for _, name := range []string{caKeyFile, sshHostKeyFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return nil, ErrClearStore
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	certPEM, certErr := os.ReadFile(filepath.Join(dir, caCertFile))
	_, sealedErr := os.Stat(filepath.Join(dir, caSealedFile))
	for _, err := range []error{certErr, sealedErr} {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	switch {
	case certErr == nil && sealedErr == nil:
		cert, err := parseCertificate(certPEM)
		if err != nil {
			return nil, err
		}
		s.ca = &CA{Cert: cert, CertPEM: certPEM}
	case certErr != nil && sealedErr != nil:
	default:
		return nil, fmt.Errorf("the CA in %s is incomplete (%s: %v, %s: %v): restore the missing file, or run `egzo project rotate-ca`",
			dir, caCertFile, certErr == nil, caSealedFile, sealedErr == nil)
	}

	if line, err := os.ReadFile(filepath.Join(dir, sshHostPublicFile)); err == nil {
		public, _, _, _, err := ssh.ParseAuthorizedKey(line)
		if err != nil {
			return nil, fmt.Errorf("ssh host public key: %w", err)
		}
		s.hostPub = public
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

// CA is the CA as far as it is known: nil when there is none yet, without its key while it is sealed.
func (s *Store) CA() *CA {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ca
}

// SSHHostKey is the ssh host key, nil until it is opened, and its public half, nil when there is none yet.
func (s *Store) SSHHostKey() (ssh.Signer, ssh.PublicKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hostKey, s.hostPub
}

// Unlock opens the store with keys: it opens what is sealed and makes what is not there yet. It reports whether the CA or the ssh host key changed, so the caller hands them to the
// server and publishes them. A store sealed with other keys is ErrWrongSecretKey, and nothing changes.
func (s *Store) Unlock(keys *seal.Keys) (changed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if keys.Same(s.keys) {
		return false, nil
	}
	ca, err := s.unlockCA(keys)
	if err != nil {
		return false, err
	}
	hostKey, err := s.unlockHostKey(keys)
	if err != nil {
		return false, err
	}
	s.keys, s.ca, s.hostKey, s.hostPub = keys, ca, hostKey, hostKey.PublicKey()
	return true, nil
}

func (s *Store) unlockCA(keys *seal.Keys) (*CA, error) {
	sealed, err := os.ReadFile(filepath.Join(s.dir, caSealedFile))
	switch {
	case err == nil:
		keyPEM, err := keys.Open(seal.UseCA, sealed)
		if errors.Is(err, seal.ErrWrongKey) {
			return nil, ErrWrongSecretKey
		}
		if err != nil {
			return nil, fmt.Errorf("the sealed CA key: %w", err)
		}
		return parseCA(keyPEM, s.ca.CertPEM)
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	ca, err := NewCA()
	if err != nil {
		return nil, err
	}
	return ca, s.saveCA(keys, ca)
}

func (s *Store) unlockHostKey(keys *seal.Keys) (ssh.Signer, error) {
	sealed, err := os.ReadFile(filepath.Join(s.dir, sshHostSealedFile))
	switch {
	case err == nil:
		data, err := keys.Open(seal.UseSSHHostKey, sealed)
		if errors.Is(err, seal.ErrWrongKey) {
			return nil, ErrWrongSecretKey
		}
		if err != nil {
			return nil, fmt.Errorf("the sealed ssh host key: %w", err)
		}
		return ssh.ParsePrivateKey(data)
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	return s.newHostKey(keys)
}

// Rotate replaces the CA and the ssh host key with new ones sealed with keys, which may differ from the keys the store
// was opened with: it is how a project moves to another secret key. publish is called with the new CA before anything is
// stored, and with the old one again if storing fails.
func (s *Store) Rotate(keys *seal.Keys, publish func(*CA) error) (*CA, ssh.Signer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ca, err := NewCA()
	if err != nil {
		return nil, nil, err
	}
	restore := func() {
		if s.ca != nil {
			publish(s.ca)
		}
	}
	if err := publish(ca); err != nil {
		restore()
		return nil, nil, fmt.Errorf("publish the new CA: %w", err)
	}
	if err := s.saveCA(keys, ca); err != nil {
		restore()
		return nil, nil, fmt.Errorf("store the new CA: %w", err)
	}
	hostKey, err := s.newHostKey(keys)
	if err != nil {
		return nil, nil, fmt.Errorf("store a new ssh host key: %w", err)
	}
	if !keys.Same(s.keys) {
		s.dropSignIns() // sealed with the old key: the project signs in again
	}
	s.keys, s.ca, s.hostKey, s.hostPub = keys, ca, hostKey, hostKey.PublicKey()
	return ca, hostKey, nil
}

// saveCA stores the certificate in clear and the key sealed. Each file is written to a temporary name and renamed.
func (s *Store) saveCA(keys *seal.Keys, ca *CA) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(ca.Key)
	if err != nil {
		return err
	}
	sealed, err := keys.Seal(seal.UseCA, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(s.dir, caSealedFile), sealed, 0o600); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.dir, caCertFile), ca.CertPEM, 0o644)
}

func (s *Store) newHostKey(keys *seal.Keys) (ssh.Signer, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(private, "egzo proxy")
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return nil, err
	}
	return signer, s.saveHostKey(keys, pem.EncodeToMemory(block), signer.PublicKey())
}

func (s *Store) saveHostKey(keys *seal.Keys, private []byte, public ssh.PublicKey) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	sealed, err := keys.Seal(seal.UseSSHHostKey, private)
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(s.dir, sshHostSealedFile), sealed, 0o600); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.dir, sshHostPublicFile), ssh.MarshalAuthorizedKey(public), 0o644)
}

// signInPrefix starts the file of a sign-in: signin-<hex of the service name>.sealed.
const signInPrefix = "signin-"

// SignIns opens the sealed sign-ins. One sealed with other keys (a store that moved to another key) is removed: it
// cannot be opened, and the user signs in again.
func (s *Store) SignIns() ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys == nil {
		return nil, errLocked
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var records [][]byte
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, signInPrefix) || !strings.HasSuffix(name, ".sealed") {
			continue
		}
		sealed, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			return nil, err
		}
		data, err := s.keys.Open(seal.UseOAuth, sealed)
		if err != nil {
			os.Remove(filepath.Join(s.dir, name))
			continue
		}
		records = append(records, data)
	}
	return records, nil
}

// SaveSignIn seals the sign-in of a service.
func (s *Store) SaveSignIn(service string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys == nil {
		return errLocked
	}
	sealed, err := s.keys.Seal(seal.UseOAuth, data)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.dir, signInPrefix+hex.EncodeToString([]byte(service))+".sealed"), sealed, 0o600)
}

// dropSignIns removes every sealed sign-in. It runs under s.mu.
func (s *Store) dropSignIns() {
	entries, _ := os.ReadDir(s.dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), signInPrefix) {
			os.Remove(filepath.Join(s.dir, entry.Name()))
		}
	}
}

func parseCertificate(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, errors.New("the CA certificate is not valid PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}
