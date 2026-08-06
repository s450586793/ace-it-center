//go:build !windows

package command

func NewPlatformRunner() (Runner, bool) {
	return nil, false
}
