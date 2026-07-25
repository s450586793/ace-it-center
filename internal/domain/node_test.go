package domain

import "testing"

func TestValidateNodeTypeRejectsUnsupportedType(t *testing.T) {
	t.Parallel()

	if err := ValidateNodeType("router"); err == nil {
		t.Fatal("ValidateNodeType(router) returned nil, want error")
	}
}

func TestValidateNodeTypeAcceptsMVPTypes(t *testing.T) {
	t.Parallel()

	for _, nodeType := range []string{"windows", "linux"} {
		if err := ValidateNodeType(nodeType); err != nil {
			t.Fatalf("ValidateNodeType(%q) returned %v", nodeType, err)
		}
	}
}

