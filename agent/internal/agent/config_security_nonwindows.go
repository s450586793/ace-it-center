//go:build !windows

package agent

import "os"

var secureConfigDirectory = func(path string) error {
	return os.Chmod(path, 0o700)
}

var secureConfigFile = func(path string) error {
	return os.Chmod(path, 0o600)
}
