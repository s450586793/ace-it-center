package security

import "testing"

func TestHashPasswordRejectsShortPassword(t *testing.T) {
	t.Parallel()

	if _, err := HashPassword("short-password"); err == nil {
		t.Fatal("HashPassword accepted a password shorter than 15 characters")
	}
}

func TestVerifyPasswordRoundTrip(t *testing.T) {
	t.Parallel()

	hash, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	if !VerifyPassword(hash, "correct-horse-battery-staple") {
		t.Fatal("VerifyPassword rejected the original password")
	}
	if VerifyPassword(hash, "incorrect-password-value") {
		t.Fatal("VerifyPassword accepted an incorrect password")
	}
}

