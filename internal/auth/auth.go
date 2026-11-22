// Package auth checks client API keys. The config stores only the SHA-256
// hash of each key, so a leaked config file does not leak usable keys.
//
// SHA-256 (not bcrypt) is enough here because keys are 256-bit random values,
// not human passwords: guessing is impossible either way, and a slow hash would
// add latency to every request.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/config"
)

// KeyPrefix marks gateway keys, so they are easy to spot (and to scan for).
const KeyPrefix = "sk-gw-"

// Key is a client key's identity and permissions. It never holds the key itself.
type Key struct {
	ID     string
	Owner  string
	Team   string
	Policy string

	allowAll bool
	aliases  map[string]bool
}

// Allows reports whether the key may use the given alias.
func (k *Key) Allows(alias string) bool {
	return k.allowAll || k.aliases[alias]
}

// Store finds keys by the hash of the raw key.
type Store struct {
	byHash map[string]*Key
}

// NewStore builds a Store from the config keys.
func NewStore(keys []config.Key) *Store {
	s := &Store{byHash: make(map[string]*Key, len(keys))}
	for _, ck := range keys {
		k := &Key{ID: ck.ID, Owner: ck.Owner, Team: ck.Team, Policy: ck.Policy, aliases: map[string]bool{}}
		for _, a := range ck.Aliases {
			if a == "*" {
				k.allowAll = true
			}
			k.aliases[a] = true
		}
		s.byHash[ck.Hash] = k
	}
	return s
}

// Lookup hashes the raw key and finds it. A map lookup on the hash does not
// leak useful timing: an attacker cannot choose what the hash of a guess is.
func (s *Store) Lookup(raw string) (*Key, bool) {
	k, ok := s.byHash[Hash(raw)]
	return k, ok
}

// Hash returns the lowercase hex SHA-256 of a raw key.
func Hash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Generate returns a new random key: the prefix plus 32 random bytes.
func Generate() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return KeyPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// BearerToken returns the key from an "Authorization: Bearer <key>" header.
// The scheme name is case-insensitive, as RFC 7235 says.
func BearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const scheme = "bearer "
	if len(h) <= len(scheme) || !strings.EqualFold(h[:len(scheme)], scheme) {
		return "", false
	}
	token := strings.TrimSpace(h[len(scheme):])
	return token, token != ""
}
