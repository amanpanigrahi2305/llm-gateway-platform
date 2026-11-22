package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/auth"
)

func TestGenKeyPrintsMatchingKeyAndHash(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"gen-key"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	var key, hash string
	for _, line := range strings.Split(out.String(), "\n") {
		if v, ok := strings.CutPrefix(line, "key:  "); ok {
			key = v
		}
		if v, ok := strings.CutPrefix(line, "hash: "); ok {
			hash = v
		}
	}
	if !strings.HasPrefix(key, auth.KeyPrefix) || hash != auth.Hash(key) {
		t.Fatalf("key %q, hash %q: hash does not match key", key, hash)
	}
}

func TestHashKeyReadsStdin(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"hash-key"}, strings.NewReader("abc\n"), &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != auth.Hash("abc") {
		t.Fatalf("hash-key printed %q", got)
	}
	if err := run([]string{"hash-key"}, strings.NewReader("  \n"), &out); err == nil {
		t.Fatal("hash-key accepted empty input")
	}
}

func TestUnknownCommand(t *testing.T) {
	if err := run([]string{"launch"}, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("unknown command was accepted")
	}
}
