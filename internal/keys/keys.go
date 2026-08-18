// Package keys manages the daemon's SSH key material: the gateway host
// key, the broker key injected into every VM, and the user-facing
// authorized_keys file.
package keys

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gossh "golang.org/x/crypto/ssh"
)

// EnsureED25519 loads the key at path, generating it on first use.
func EnsureED25519(path, comment string) (gossh.Signer, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		signer, err := gossh.ParsePrivateKey(data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		return signer, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := gossh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, err
	}
	return gossh.NewSignerFromKey(priv)
}

// AuthorizedLine renders a public key as an authorized_keys line.
func AuthorizedLine(pub gossh.PublicKey, comment string) string {
	line := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(pub)))
	if comment != "" {
		line += " " + comment
	}
	return line
}

// LoadAuthorizedKeys parses the authorized_keys file at path; a missing
// file is an empty list.
func LoadAuthorizedKeys(path string) ([]gossh.PublicKey, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []gossh.PublicKey
	for rest := data; len(rest) > 0; {
		key, _, _, next, err := gossh.ParseAuthorizedKey(rest)
		if err != nil {
			// Skip unparsable line.
			if i := indexByte(rest, '\n'); i >= 0 {
				rest = rest[i+1:]
				continue
			}
			break
		}
		out = append(out, key)
		rest = next
	}
	return out, nil
}

// AuthorizedLines returns the raw authorized_keys lines (for delivery to
// guests).
func AuthorizedLines(path string) ([]string, error) {
	pubs, err := LoadAuthorizedKeys(path)
	if err != nil {
		return nil, err
	}
	lines := make([]string, 0, len(pubs))
	for _, p := range pubs {
		lines = append(lines, AuthorizedLine(p, ""))
	}
	return lines, nil
}

// Seed appends the user's ~/.ssh/*.pub keys to the authorized_keys file at
// path, deduplicating. Returns how many keys the file holds afterwards.
func Seed(path string) (int, error) {
	existing, err := LoadAuthorizedKeys(path)
	if err != nil {
		return 0, err
	}
	seen := map[string]bool{}
	for _, k := range existing {
		seen[string(k.Marshal())] = true
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return len(existing), err
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".ssh", "*.pub"))

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return len(existing), err
	}
	defer f.Close()

	count := len(existing)
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		key, comment, _, _, err := gossh.ParseAuthorizedKey(data)
		if err != nil || seen[string(key.Marshal())] {
			continue
		}
		seen[string(key.Marshal())] = true
		if comment == "" {
			comment = filepath.Base(m)
		}
		fmt.Fprintln(f, AuthorizedLine(key, comment))
		count++
	}
	return count, nil
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}
