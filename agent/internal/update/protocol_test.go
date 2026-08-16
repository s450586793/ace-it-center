package update

import (
	"bytes"
	"strings"
	"testing"
)

func TestCheckResultRoundTrip(t *testing.T) {
	want := CheckResult{
		Available:     true,
		Version:       "0.4.11",
		URL:           "http://it.example:1111/downloads/windows/stable/AceAgentSetup-windows-amd64-V0.4.11.exe",
		InstallerPath: `C:\ProgramData\AceITCenter\updates\AceAgentSetup-windows-amd64-V0.4.11.exe`,
	}
	var output bytes.Buffer
	if err := EncodeCheckResult(&output, want); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeCheckResult(bytes.NewReader(output.Bytes()))
	if err != nil || got != want {
		t.Fatalf("DecodeCheckResult() = %#v, %v; want %#v", got, err, want)
	}
}

func TestCheckResultRejectsMalformedOrInconsistentData(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "unknown field", input: `{"available":false,"extra":true}`},
		{name: "multiple values", input: `{"available":false} {"available":false}`},
		{name: "unavailable with version", input: `{"available":false,"version":"0.4.11"}`},
		{name: "available without version", input: `{"available":true,"url":"https://it.example/agent.exe","installer_path":"C:\\updates\\agent.exe"}`},
		{name: "available without URL", input: `{"available":true,"version":"0.4.11","installer_path":"C:\\updates\\agent.exe"}`},
		{name: "available with credential URL", input: `{"available":true,"version":"0.4.11","url":"https://user@it.example/agent.exe","installer_path":"C:\\updates\\agent.exe"}`},
		{name: "available with query URL", input: `{"available":true,"version":"0.4.11","url":"https://it.example/agent.exe?token=secret","installer_path":"C:\\updates\\agent.exe"}`},
		{name: "available with relative path", input: `{"available":true,"version":"0.4.11","url":"https://it.example/agent.exe","installer_path":"updates/agent.exe"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeCheckResult(strings.NewReader(test.input)); err == nil {
				t.Fatal("DecodeCheckResult() accepted invalid input")
			}
		})
	}
}

func TestCheckResultRejectsOversizedOutput(t *testing.T) {
	input := `{"available":false,"padding":"` + strings.Repeat("x", MaxCheckResultBytes) + `"}`
	if _, err := DecodeCheckResult(strings.NewReader(input)); err == nil {
		t.Fatal("DecodeCheckResult() accepted oversized output")
	}
}

func TestEncodeCheckResultRejectsInvalidValue(t *testing.T) {
	invalid := CheckResult{Available: true, Version: "0.4", URL: "https://it.example/agent.exe", InstallerPath: `C:\updates\agent.exe`}
	if err := EncodeCheckResult(&bytes.Buffer{}, invalid); err == nil {
		t.Fatal("EncodeCheckResult() accepted invalid value")
	}
}
