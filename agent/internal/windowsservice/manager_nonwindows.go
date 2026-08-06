//go:build !windows

package windowsservice

import "fmt"

func Install(string) error {
	return fmt.Errorf("Windows Service is only supported on Windows")
}

func RestoreServiceExecutable(string) error {
	return fmt.Errorf("Windows Service is only supported on Windows")
}

func Stop() error {
	return fmt.Errorf("Windows Service is only supported on Windows")
}

func Uninstall() error {
	return fmt.Errorf("Windows Service is only supported on Windows")
}
