package auth

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/config"
)

func TestHashKnownValue(t *testing.T) {
	// SHA-256("abc") from FIPS 180-2.
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := Hash("abc"); got != want {
		t.Fatalf("Hash(abc) = %s, want %s", got, want)
	}
}

func TestGenerate(t *testing.T) {
	a, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Generate()
	if !strings.HasPrefix(a, KeyPrefix) {
		t.Errorf("key %q lacks prefix %q", a, KeyPrefix)
	}
	if len(a) != len(KeyPrefix)+43 { // 32 bytes in unpadded base64url = 43 chars
		t.Errorf("key length = %d, want %d", len(a), len(KeyPrefix)+43)
	}
	if a == b {
		t.Error("two generated keys are equal")
	}
}

func TestStoreLookupAndAllows(t *testing.T) {
	store := NewStore([]config.Key{
		{ID: "all", Hash: Hash("key-all"), Aliases: []string{"*"}},
		{ID: "fast-only", Hash: Hash("key-fast"), Team: "web", Aliases: []string{"fast"}},
	})

	if _, ok := store.Lookup("wrong-key"); ok {
		t.Fatal("unknown key was accepted")
	}
	k, ok := store.Lookup("key-fast")
	if !ok || k.ID != "fast-only" || k.Team != "web" {
		t.Fatalf("Lookup(key-fast) = %+v, %v", k, ok)
	}
	if !k.Allows("fast") || k.Allows("smart") {
		t.Error("fast-only key: want fast allowed and smart denied")
	}
	all, _ := store.Lookup("key-all")
	if !all.Allows("anything") {
		t.Error(`key with "*" should allow every alias`)
	}
}

func TestBearerToken(t *testing.T) {
	tests := []struct {
		header, want string
		ok           bool
	}{
		{"Bearer sk-gw-abc", "sk-gw-abc", true},
		{"bearer sk-gw-abc", "sk-gw-abc", true},
		{"Bearer   sk-gw-abc  ", "sk-gw-abc", true},
		{"Bearer ", "", false},
		{"Basic dXNlcjpwYXNz", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("GET", "/", nil)
		if tt.header != "" {
			r.Header.Set("Authorization", tt.header)
		}
		got, ok := BearerToken(r)
		if got != tt.want || ok != tt.ok {
			t.Errorf("BearerToken(%q) = %q, %v; want %q, %v", tt.header, got, ok, tt.want, tt.ok)
		}
	}
}
