package release

import "testing"

func TestCompareVersionsUsesSemanticVersionPrecedence(t *testing.T) {
	tests := []struct {
		candidate string
		current   string
		want      int
	}{
		{candidate: "0.4.11", current: "0.4.10", want: 1},
		{candidate: "0.4.10", current: "0.4.10", want: 0},
		{candidate: "0.4.9", current: "0.4.10", want: -1},
		{candidate: "1.0.0", current: "1.0.0-rc.1", want: 1},
	}
	for _, test := range tests {
		got, err := CompareVersions(test.candidate, test.current)
		if err != nil || got != test.want {
			t.Fatalf("CompareVersions(%q, %q) = %d, %v; want %d", test.candidate, test.current, got, err, test.want)
		}
	}
}

func TestCompareVersionsRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		candidate string
		current   string
	}{
		{candidate: "0.4", current: "0.4.10"},
		{candidate: "0.4.11", current: "development"},
	}
	for _, test := range tests {
		if _, err := CompareVersions(test.candidate, test.current); err == nil {
			t.Fatalf("CompareVersions(%q, %q) accepted invalid input", test.candidate, test.current)
		}
	}
}
