package security

import "testing"

func TestNewOpaqueTokenReturnsPlaintextAndStableHash(t *testing.T) {
	t.Parallel()

	plain, hash, err := NewOpaqueToken()
	if err != nil {
		t.Fatalf("NewOpaqueToken returned error: %v", err)
	}
	if plain == "" || hash == "" {
		t.Fatal("NewOpaqueToken returned an empty value")
	}
	if plain == hash {
		t.Fatal("NewOpaqueToken returned plaintext as its stored hash")
	}
	if got := HashToken(plain); got != hash {
		t.Fatalf("HashToken(plain) = %q, want %q", got, hash)
	}
}

func TestNewOpaqueTokenProducesUniqueValues(t *testing.T) {
	t.Parallel()

	first, _, err := NewOpaqueToken()
	if err != nil {
		t.Fatalf("first NewOpaqueToken returned error: %v", err)
	}
	second, _, err := NewOpaqueToken()
	if err != nil {
		t.Fatalf("second NewOpaqueToken returned error: %v", err)
	}
	if first == second {
		t.Fatal("NewOpaqueToken returned duplicate values")
	}
}
