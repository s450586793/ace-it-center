package core

import (
	"strings"
	"testing"
)

func TestValidateCommandAcceptsWindowsShells(t *testing.T) {
	t.Parallel()

	for _, shell := range []CommandShell{CommandShellPowerShell, CommandShellCMD} {
		if err := ValidateCommand(shell, "hostname", 300); err != nil {
			t.Fatalf("ValidateCommand(%q): %v", shell, err)
		}
	}
}

func TestValidateCommandRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		shell          CommandShell
		command        string
		timeoutSeconds int
	}{
		{name: "unknown shell", shell: "shell", command: "hostname", timeoutSeconds: 300},
		{name: "empty command", shell: CommandShellPowerShell, command: " \r\n\t", timeoutSeconds: 300},
		{name: "short timeout", shell: CommandShellCMD, command: "hostname", timeoutSeconds: 9},
		{name: "long timeout", shell: CommandShellCMD, command: "hostname", timeoutSeconds: 1801},
		{name: "oversized command", shell: CommandShellPowerShell, command: strings.Repeat("x", MaxCommandBytes+1), timeoutSeconds: 300},
		{name: "invalid utf8", shell: CommandShellCMD, command: string([]byte{0xff}), timeoutSeconds: 300},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateCommand(test.shell, test.command, test.timeoutSeconds); err == nil {
				t.Fatalf("ValidateCommand(%q, %q, %d) accepted invalid input", test.shell, test.command, test.timeoutSeconds)
			}
		})
	}
}

func TestCommandStatusTerminal(t *testing.T) {
	t.Parallel()

	for _, status := range []CommandStatus{CommandQueued, CommandLeased, CommandRunning} {
		if status.Terminal() {
			t.Fatalf("%q must not be terminal", status)
		}
	}
	for _, status := range []CommandStatus{CommandSucceeded, CommandFailed, CommandTimedOut} {
		if !status.Terminal() {
			t.Fatalf("%q must be terminal", status)
		}
	}
	if CommandStatus("unknown").Terminal() {
		t.Fatal("unknown status must not be terminal")
	}
}
