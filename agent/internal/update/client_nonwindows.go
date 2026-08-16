//go:build !windows

package update

func defaultProcessRunner() ProcessRunner { return nil }
