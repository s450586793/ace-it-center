//go:build windows

package agent

import (
	"fmt"

	"golang.org/x/sys/windows"
)

const (
	configDirectorySDDL = "D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	configFileSDDL      = "D:P(A;;FA;;;SY)(A;;FA;;;BA)"
)

var applyConfigACL = applyWindowsConfigACL

var secureConfigDirectory = func(path string) error {
	return applyConfigACL(path, configDirectorySDDL)
}

var secureConfigFile = func(path string) error {
	return applyConfigACL(path, configFileSDDL)
}

func applyWindowsConfigACL(path, sddl string) error {
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("parse config ACL: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read config ACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf("apply config ACL: %w", err)
	}
	return nil
}
