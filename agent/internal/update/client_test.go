package update

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

type fakeProcessRunner struct {
	executable string
	args       []string
	maximum    int
	output     []byte
	runErr     error
	detached   DetachedLaunchOptions
	startErr   error
}

func (runner *fakeProcessRunner) Run(_ context.Context, executable string, args []string, maximumOutput int) ([]byte, error) {
	runner.executable = executable
	runner.args = append([]string(nil), args...)
	runner.maximum = maximumOutput
	return append([]byte(nil), runner.output...), runner.runErr
}

func (runner *fakeProcessRunner) StartDetached(_ context.Context, executable string, args []string, options DetachedLaunchOptions) error {
	runner.executable = executable
	runner.args = append([]string(nil), args...)
	runner.detached = options
	return runner.startErr
}

func TestProcessClientCheckUsesFixedUpdaterAndPublicArguments(t *testing.T) {
	var output bytes.Buffer
	if err := EncodeCheckResult(&output, CheckResult{Available: true, Version: "0.4.11", URL: "https://it.example/agent.exe", InstallerPath: `C:\ProgramData\AceITCenter\updates\setup.exe`}); err != nil {
		t.Fatal(err)
	}
	runner := &fakeProcessRunner{output: output.Bytes()}
	client := ProcessClient{AgentPath: `C:\Program Files\Ace IT Center\AceAgent.exe`, Runner: runner}

	result, err := client.Check(context.Background(), CheckOptions{
		Origin:         "https://it.example",
		CurrentVersion: "0.4.10",
		CurrentOS:      "10.0.19045",
		StagingDir:     `C:\ProgramData\AceITCenter\updates`,
	})

	if err != nil || !result.Available || result.Version != "0.4.11" {
		t.Fatalf("Check() = %#v, %v", result, err)
	}
	if runner.executable != `C:\Program Files\Ace IT Center\AceAgentUpdater.exe` {
		t.Fatalf("executable = %q", runner.executable)
	}
	wantArgs := []string{"check", "--origin", "https://it.example", "--current-version", "0.4.10", "--current-os", "10.0.19045", "--staging", `C:\ProgramData\AceITCenter\updates`}
	if !slices.Equal(runner.args, wantArgs) || runner.maximum != MaxCheckResultBytes {
		t.Fatalf("args = %#v, maximum = %d", runner.args, runner.maximum)
	}
}

func TestProcessClientLaunchApplyIsDetachedAndCredentialFree(t *testing.T) {
	runner := &fakeProcessRunner{}
	client := ProcessClient{AgentPath: `C:\Program Files\Ace IT Center\AceAgent.exe`, Runner: runner}
	options := ApplyOptions{
		InstallerPath: `C:\ProgramData\AceITCenter\updates\setup.exe`,
		BackupPath:    `C:\ProgramData\AceITCenter\updates\AceAgent.lkg.exe`,
		Version:       "0.4.11",
	}

	if err := client.LaunchApply(context.Background(), options); err != nil {
		t.Fatalf("LaunchApply() error = %v", err)
	}
	wantArgs := []string{"apply", "--installer", options.InstallerPath, "--agent", client.AgentPath, "--backup", options.BackupPath, "--version", options.Version}
	if !slices.Equal(runner.args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", runner.args, wantArgs)
	}
	if !runner.detached.NewProcessGroup || !runner.detached.Detached || !runner.detached.BreakawayFromJob {
		t.Fatalf("detached options = %#v", runner.detached)
	}
	for _, argument := range runner.args {
		if strings.Contains(strings.ToLower(argument), "credential") || strings.Contains(argument, "secret") {
			t.Fatalf("credential-like argument = %q", argument)
		}
	}
}

func TestProcessClientRejectsMalformedOrOversizedCheckOutput(t *testing.T) {
	tests := [][]byte{
		[]byte(`{"available":false,"unknown":true}`),
		[]byte(strings.Repeat("x", MaxCheckResultBytes+1)),
	}
	for _, output := range tests {
		runner := &fakeProcessRunner{output: output}
		client := ProcessClient{AgentPath: "/program/AceAgent.exe", Runner: runner}
		_, err := client.Check(context.Background(), CheckOptions{Origin: "https://it.example", CurrentVersion: "0.4.10", CurrentOS: "10.0.19045", StagingDir: "/updates"})
		if err == nil {
			t.Fatal("Check() accepted invalid updater output")
		}
	}
}

func TestProcessClientPropagatesMissingUpdaterProcessFailureAndCancellation(t *testing.T) {
	for _, wantErr := range []error{os.ErrNotExist, context.Canceled} {
		runner := &fakeProcessRunner{runErr: wantErr}
		client := ProcessClient{AgentPath: "/program/AceAgent.exe", Runner: runner}
		_, err := client.Check(context.Background(), CheckOptions{Origin: "https://it.example", CurrentVersion: "0.4.10", CurrentOS: "10.0.19045", StagingDir: "/updates"})
		if !errors.Is(err, wantErr) {
			t.Fatalf("Check() error = %v, want %v", err, wantErr)
		}
	}
}

func TestProcessClientRejectsIncompleteOptionsBeforeStarting(t *testing.T) {
	runner := &fakeProcessRunner{}
	client := ProcessClient{AgentPath: "/program/AceAgent.exe", Runner: runner}
	if _, err := client.Check(context.Background(), CheckOptions{}); err == nil {
		t.Fatal("Check() accepted incomplete options")
	}
	if err := client.LaunchApply(context.Background(), ApplyOptions{InstallerPath: "relative", BackupPath: "/updates/lkg.exe", Version: "0.4.11"}); err == nil {
		t.Fatal("LaunchApply() accepted a relative path")
	}
	if runner.executable != "" {
		t.Fatalf("started executable = %q", runner.executable)
	}
}

func TestProcessClientFailsClosedWithoutPlatformRunner(t *testing.T) {
	client := ProcessClient{AgentPath: "/program/AceAgent.exe"}
	_, err := client.Check(context.Background(), CheckOptions{Origin: "https://it.example", CurrentVersion: "0.4.10", CurrentOS: "10.0.19045", StagingDir: "/updates"})
	if err == nil {
		t.Fatal("Check() used an unavailable platform runner")
	}
}

func TestProcessClientValidatesContextsVersionsAndDetachedFailure(t *testing.T) {
	runner := &fakeProcessRunner{startErr: os.ErrPermission}
	client := ProcessClient{AgentPath: "/program/AceAgent.exe", Runner: runner}
	if _, err := client.Check(nil, CheckOptions{}); err == nil {
		t.Fatal("Check() accepted nil context")
	}
	if err := client.LaunchApply(nil, ApplyOptions{}); err == nil {
		t.Fatal("LaunchApply() accepted nil context")
	}
	if _, err := (ProcessClient{AgentPath: "relative", Runner: runner}).Check(context.Background(), CheckOptions{}); err == nil {
		t.Fatal("Check() accepted relative Agent path")
	}
	if err := client.LaunchApply(context.Background(), ApplyOptions{InstallerPath: "/updates/setup.exe", BackupPath: "/updates/lkg.exe", Version: "invalid"}); err == nil {
		t.Fatal("LaunchApply() accepted invalid version")
	}
	err := client.LaunchApply(context.Background(), ApplyOptions{InstallerPath: "/updates/setup.exe", BackupPath: "/updates/lkg.exe", Version: "0.4.11"})
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("LaunchApply() error = %v", err)
	}
}

func TestBoundedProcessBufferKeepsPrefixAndReportsOverflow(t *testing.T) {
	buffer := &boundedProcessBuffer{maximum: 4}
	if count, err := buffer.Write([]byte("abc")); err != nil || count != 3 {
		t.Fatalf("first Write() = %d, %v", count, err)
	}
	if count, err := buffer.Write([]byte("def")); err != nil || count != 3 {
		t.Fatalf("second Write() = %d, %v", count, err)
	}
	if buffer.String() != "abcd" || !buffer.exceeded {
		t.Fatalf("buffer = %q, exceeded=%t", buffer.String(), buffer.exceeded)
	}
}
