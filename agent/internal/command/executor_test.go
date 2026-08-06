package command

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"aceitcenter.local/platform/internal/core"
)

type fakeRunner struct {
	invocation     Invocation
	output         []byte
	exitCode       int
	err            error
	waitForContext bool
	calls          int
}

func (f *fakeRunner) Run(ctx context.Context, invocation Invocation, output io.Writer) (int, error) {
	f.calls++
	f.invocation = invocation
	if f.waitForContext {
		<-ctx.Done()
		return -1, ctx.Err()
	}
	if len(f.output) > 0 {
		if _, err := output.Write(f.output); err != nil {
			return -1, err
		}
	}
	return f.exitCode, f.err
}

func TestExecutorRunsPowerShellWithFixedArguments(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{output: []byte("office-pc\n")}
	result := NewExecutor(runner).Execute(context.Background(), core.CommandClaim{
		Shell: core.CommandShellPowerShell, Command: "hostname", TimeoutSeconds: 300,
	})

	if runner.invocation.Program != "powershell.exe" {
		t.Fatalf("program=%q", runner.invocation.Program)
	}
	wantArgs := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "hostname"}
	if !reflect.DeepEqual(runner.invocation.Args, wantArgs) {
		t.Fatalf("args=%v want=%v", runner.invocation.Args, wantArgs)
	}
	if result.Status != core.CommandSucceeded || result.ExitCode == nil || *result.ExitCode != 0 || result.Output != "office-pc\n" {
		t.Fatalf("result=%#v", result)
	}
}

func TestExecutorMapsCMDNonZeroExitToFailure(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{exitCode: 7, err: errors.New("exit status 7")}
	result := NewExecutor(runner).Execute(context.Background(), core.CommandClaim{
		Shell: core.CommandShellCMD, Command: "exit /b 7", TimeoutSeconds: 300,
	})

	if runner.invocation.Program != "cmd.exe" || !reflect.DeepEqual(runner.invocation.Args, []string{"/D", "/S", "/C", "exit /b 7"}) {
		t.Fatalf("invocation=%#v", runner.invocation)
	}
	if result.Status != core.CommandFailed || result.ExitCode == nil || *result.ExitCode != 7 || !strings.Contains(result.ErrorMessage, "code 7") {
		t.Fatalf("result=%#v", result)
	}
}

func TestExecutorTimesOutCommand(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{waitForContext: true}
	executor := NewExecutor(runner)
	executor.timeout = func(int) time.Duration { return time.Millisecond }
	result := executor.Execute(context.Background(), core.CommandClaim{
		Shell: core.CommandShellPowerShell, Command: "Start-Sleep -Seconds 20", TimeoutSeconds: core.MinCommandTimeout,
	})

	if result.Status != core.CommandTimedOut || !strings.Contains(result.ErrorMessage, "timed out") {
		t.Fatalf("result=%#v", result)
	}
}

func TestExecutorTruncatesAndNormalizesOutput(t *testing.T) {
	t.Parallel()

	output := append([]byte{0xff}, []byte(strings.Repeat("x", core.MaxCommandOutputBytes+128))...)
	runner := &fakeRunner{output: output}
	result := NewExecutor(runner).Execute(context.Background(), core.CommandClaim{
		Shell: core.CommandShellCMD, Command: "echo output", TimeoutSeconds: 300,
	})

	if !result.OutputTruncated || len(result.Output) > core.MaxCommandOutputBytes || !utf8.ValidString(result.Output) {
		t.Fatalf("output bytes=%d truncated=%v valid=%v", len(result.Output), result.OutputTruncated, utf8.ValidString(result.Output))
	}
}

func TestExecutorRejectsInvalidClaimWithoutStartingProcess(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	result := NewExecutor(runner).Execute(context.Background(), core.CommandClaim{
		Shell: "python", Command: "print('no')", TimeoutSeconds: 300,
	})

	if runner.calls != 0 || result.Status != core.CommandFailed || result.ErrorMessage == "" {
		t.Fatalf("runner calls=%d result=%#v", runner.calls, result)
	}
}

func TestBoundedOutputWriterContinuesAcceptingDiscardedBytes(t *testing.T) {
	t.Parallel()

	writer := newBoundedOutputWriter(4)
	written, err := writer.Write([]byte("123456"))
	if err != nil || written != 6 || string(writer.Bytes()) != "1234" || !writer.Truncated() {
		t.Fatalf("Write=(%d,%v) bytes=%q truncated=%v", written, err, writer.Bytes(), writer.Truncated())
	}
}

var _ io.Writer = (*boundedOutputWriter)(nil)
