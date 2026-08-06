//go:build !windows

package update

import "os"

func replaceStagedFile(sourcePath, destinationPath string) error {
	return os.Rename(sourcePath, destinationPath)
}
