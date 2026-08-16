package app

import (
	"slices"
	"testing"
)

func TestParseModeDefaults(t *testing.T) {
	windows, _, err := ParseMode("windows", nil)
	if err != nil || windows != ModeTray {
		t.Fatalf("windows mode = %q, err=%v", windows, err)
	}

	linux, args, err := ParseMode("linux", []string{"-once"})
	if err != nil || linux != ModeForeground || !slices.Equal(args, []string{"-once"}) {
		t.Fatalf("linux mode=%q args=%v err=%v", linux, args, err)
	}
}

func TestParseModeRecognizesWindowsModes(t *testing.T) {
	tests := []struct {
		arg  string
		want Mode
	}{
		{arg: "service", want: ModeService},
		{arg: "tray", want: ModeTray},
		{arg: "diagnose", want: ModeDiagnose},
		{arg: "version", want: ModeVersion},
	}

	for _, test := range tests {
		t.Run(test.arg, func(t *testing.T) {
			mode, args, err := ParseMode("windows", []string{test.arg, "--verbose"})
			if err != nil || mode != test.want || !slices.Equal(args, []string{"--verbose"}) {
				t.Fatalf("ParseMode() = (%q, %v, %v)", mode, args, err)
			}
		})
	}
}

func TestParseModeRejectsUnknownWindowsMode(t *testing.T) {
	for _, mode := range []string{"unknown", "update-helper"} {
		_, _, err := ParseMode("windows", []string{mode})
		if err == nil {
			t.Fatalf("expected %q mode error", mode)
		}
	}
}
