// Package app contains platform-neutral Agent runtime behavior.
package app

import "fmt"

type Mode string

const (
	ModeForeground   Mode = "foreground"
	ModeService      Mode = "service"
	ModeTray         Mode = "tray"
	ModeDiagnose     Mode = "diagnose"
	ModeVersion      Mode = "version"
	ModeUpdateHelper Mode = "update-helper"
)

func ParseMode(goos string, args []string) (Mode, []string, error) {
	if goos != "windows" {
		return ModeForeground, args, nil
	}
	if len(args) == 0 {
		return ModeTray, nil, nil
	}

	switch args[0] {
	case "service", "tray", "diagnose", "version", "update-helper":
		return Mode(args[0]), args[1:], nil
	default:
		return "", nil, fmt.Errorf("unknown Ace Agent mode %q", args[0])
	}
}
