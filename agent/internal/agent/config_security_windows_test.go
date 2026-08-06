//go:build windows

package agent

import "testing"

func TestSecureConfigPathsUseProtectedSystemAdministratorACL(t *testing.T) {
	original := applyConfigACL
	t.Cleanup(func() { applyConfigACL = original })

	var calls []struct {
		path string
		sddl string
	}
	applyConfigACL = func(path, sddl string) error {
		calls = append(calls, struct {
			path string
			sddl string
		}{path: path, sddl: sddl})
		return nil
	}

	if err := secureConfigDirectory(`C:\ProgramData\AceITCenter`); err != nil {
		t.Fatalf("secureConfigDirectory returned error: %v", err)
	}
	if err := secureConfigFile(`C:\ProgramData\AceITCenter\agent.json`); err != nil {
		t.Fatalf("secureConfigFile returned error: %v", err)
	}
	if len(calls) != 2 || calls[0].sddl != configDirectorySDDL || calls[1].sddl != configFileSDDL {
		t.Fatalf("ACL calls = %#v", calls)
	}
}
