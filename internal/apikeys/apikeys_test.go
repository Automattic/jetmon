package apikeys

import (
	"strings"
	"testing"
)

func TestGenerateTokenFormat(t *testing.T) {
	raw, hashed, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if !strings.HasPrefix(raw, TokenPrefix) {
		t.Fatalf("token missing prefix: %q", raw)
	}
	// 32 random bytes → 52 base32 chars (no padding) + "jm_" = 55.
	if len(raw) != len(TokenPrefix)+52 {
		t.Fatalf("token length = %d, want %d", len(raw), len(TokenPrefix)+52)
	}
	if len(hashed) != 64 {
		t.Fatalf("hash length = %d, want 64 (sha256 hex)", len(hashed))
	}
	if HashToken(raw) != hashed {
		t.Fatal("HashToken doesn't match GenerateToken's returned hash")
	}
}

func TestGenerateTokenUnique(t *testing.T) {
	a, _, _ := GenerateToken()
	b, _, _ := GenerateToken()
	if a == b {
		t.Fatal("two generated tokens collided — entropy source is broken")
	}
}

func TestScopeIncludes(t *testing.T) {
	cases := []struct {
		have     Scope
		required Scope
		want     bool
	}{
		{ScopeRead, ScopeRead, true},
		{ScopeRead, ScopeWrite, false},
		{ScopeRead, ScopeAdmin, false},
		{ScopeWrite, ScopeRead, true},
		{ScopeWrite, ScopeWrite, true},
		{ScopeWrite, ScopeAdmin, false},
		{ScopeAdmin, ScopeRead, true},
		{ScopeAdmin, ScopeWrite, true},
		{ScopeAdmin, ScopeAdmin, true},
	}
	for _, c := range cases {
		got := c.have.Includes(c.required)
		if got != c.want {
			t.Errorf("Scope(%q).Includes(%q) = %v, want %v", c.have, c.required, got, c.want)
		}
	}
}

func TestScopeValid(t *testing.T) {
	for _, s := range AllScopes() {
		if !s.Valid() {
			t.Errorf("AllScopes()[%q].Valid() = false", s)
		}
	}
	if Scope("anything-else").Valid() {
		t.Error("invalid scope should not be Valid()")
	}
	if Scope("").Valid() {
		t.Error("empty scope should not be Valid()")
	}
}

func TestHashTokenStability(t *testing.T) {
	// HashToken must be deterministic — Lookup compares the hash of an
	// incoming token against the stored hash, so a non-deterministic hash
	// would break auth entirely.
	a := HashToken("jm_some-fixed-token")
	b := HashToken("jm_some-fixed-token")
	if a != b {
		t.Fatal("HashToken is not deterministic")
	}
}
