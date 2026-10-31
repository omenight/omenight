package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// LoadOrCreate returns the ed25519 identity at path, creating it if absent.
func LoadOrCreate(path string) (ssh.Signer, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		return ssh.ParsePrivateKey(b)
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}

func Fingerprint(key ssh.PublicKey) string {
	return ssh.FingerprintSHA256(key)
}

// AuthorizedKey is the public half, for enrollment.
func AuthorizedKey(s ssh.Signer) string {
	return string(ssh.MarshalAuthorizedKey(s.PublicKey()))
}

// HostKeyChecker pins the relay's host key on first use (TOFU) and refuses
// any later change. One file per relay.
func HostKeyChecker(path string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		want, err := os.ReadFile(path)
		if err == nil && len(want) > 0 {
			rest := want
			for len(rest) > 0 {
				pk, _, _, next, perr := ssh.ParseAuthorizedKey(rest)
				if perr != nil {
					break
				}
				if string(pk.Marshal()) == string(key.Marshal()) {
					return nil
				}
				rest = next
			}
			return fmt.Errorf("host key mismatch for %s (%s): refusing", hostname, Fingerprint(key))
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, ssh.MarshalAuthorizedKey(key), 0o600); err != nil {
			return err
		}
		log.Printf("pinned %s host key %s", hostname, Fingerprint(key))
		return nil
	}
}
