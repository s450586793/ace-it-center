//go:build !windows

package update

import (
	"fmt"
	"os"
)

func defaultHelperOperations(HelperOptions) HelperOperations { return nil }

func defaultHelperRuntime() HelperRuntime { return nil }

func recordCleanupWarning(string, error) {}

func CurrentOSVersion() (string, error) {
	return "", fmt.Errorf("Windows version is only available on Windows")
}

func secureStagingDirectory(path string) error { return os.Chmod(path, 0o700) }

func secureStagingFile(path string) error { return os.Chmod(path, 0o600) }

func syncStagingDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
