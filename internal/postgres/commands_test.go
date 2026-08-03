package postgres

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"aceitcenter.local/platform/internal/core"
	"github.com/DATA-DOG/go-sqlmock"
)

var commandTestNow = time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

func TestCreateCommandCreatesTaskAndQueuedExecutionsAtomically(t *testing.T) {
	t.Parallel()

	store, mock := newMockStore(t)
	task := core.CommandTask{
		ID: "task-1", Shell: core.CommandShellPowerShell, Command: "hostname",
		TimeoutSeconds: 300, CreatedBy: "owner-1", CreatedAt: commandTestNow,
	}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COUNT\\(\\*\\), COUNT\\(\\*\\) FILTER").
		WithArgs("node-1", "node-2").
		WillReturnRows(sqlmock.NewRows([]string{"target_count", "windows_count"}).AddRow(2, 2))
	mock.ExpectExec("INSERT INTO command_tasks").
		WithArgs("task-1", core.CommandShellPowerShell, "hostname", 300, "owner-1", nil, commandTestNow).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO command_executions").
		WithArgs(sqlmock.AnyArg(), "task-1", "node-1", core.CommandQueued, sqlmock.AnyArg(), "task-1", "node-2", core.CommandQueued).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	detail, err := store.CreateCommand(context.Background(), task, []string{"node-1", "node-2"})
	if err != nil {
		t.Fatalf("CreateCommand returned error: %v", err)
	}
	if detail.Task.ID != "task-1" || detail.Task.TargetCount != 2 || detail.Task.Counts.Queued != 2 {
		t.Fatalf("task detail = %#v", detail.Task)
	}
	if len(detail.Executions) != 2 || detail.Executions[0].Status != core.CommandQueued || detail.Executions[1].Status != core.CommandQueued {
		t.Fatalf("executions = %#v", detail.Executions)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("create command expectations: %v", err)
	}
}

func TestCreateCommandRejectsIncompleteWindowsTargetSet(t *testing.T) {
	t.Parallel()

	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COUNT\\(\\*\\), COUNT\\(\\*\\) FILTER").
		WithArgs("node-1", "linux-1").
		WillReturnRows(sqlmock.NewRows([]string{"target_count", "windows_count"}).AddRow(2, 1))
	mock.ExpectRollback()

	_, err := store.CreateCommand(context.Background(), core.CommandTask{
		ID: "task-1", Shell: core.CommandShellCMD, Command: "hostname", TimeoutSeconds: 300,
		CreatedBy: "owner-1", CreatedAt: commandTestNow,
	}, []string{"node-1", "linux-1"})
	if err != core.ErrNotFound {
		t.Fatalf("CreateCommand error = %v, want %v", err, core.ErrNotFound)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("reject target expectations: %v", err)
	}
}

func TestListCommandsScansStatusSummary(t *testing.T) {
	t.Parallel()

	store, mock := newMockStore(t)
	mock.ExpectQuery("SELECT t.id, t.shell, t.command").
		WithArgs(100).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "shell", "command", "timeout_seconds", "created_by", "retried_from_id", "created_at",
			"target_count", "queued", "leased", "running", "succeeded", "failed", "timed_out",
		}).AddRow("task-1", "powershell", "hostname", 300, "owner-1", nil, commandTestNow, 3, 1, 0, 1, 1, 0, 0))

	items, err := store.ListCommands(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListCommands returned error: %v", err)
	}
	if len(items) != 1 || items[0].ID != "task-1" || items[0].Counts.Queued != 1 || items[0].Counts.Running != 1 || items[0].Counts.Succeeded != 1 {
		t.Fatalf("items = %#v", items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("list command expectations: %v", err)
	}
}

func TestGetCommandReturnsExecutionDetails(t *testing.T) {
	t.Parallel()

	store, mock := newMockStore(t)
	finishedAt := commandTestNow.Add(time.Second)
	exitCode := 0
	duration := int64(1000)
	mock.ExpectQuery("SELECT t.id, t.shell, t.command").
		WithArgs("task-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "shell", "command", "timeout_seconds", "created_by", "retried_from_id", "created_at",
			"target_count", "queued", "leased", "running", "succeeded", "failed", "timed_out",
		}).AddRow("task-1", "cmd", "hostname", 300, "owner-1", nil, commandTestNow, 1, 0, 0, 0, 1, 0, 0))
	mock.ExpectQuery("SELECT e.id, e.task_id, e.node_id, n.name").
		WithArgs("task-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "task_id", "node_id", "node_name", "status", "attempt", "started_at", "finished_at",
			"exit_code", "output", "output_truncated", "error_message", "duration_ms",
		}).AddRow("execution-1", "task-1", "node-1", "office-pc", "succeeded", 1, commandTestNow, finishedAt, exitCode, "office-pc", false, "", duration))

	detail, err := store.GetCommand(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("GetCommand returned error: %v", err)
	}
	if len(detail.Executions) != 1 || detail.Executions[0].NodeName != "office-pc" || detail.Executions[0].ExitCode == nil || *detail.Executions[0].ExitCode != 0 {
		t.Fatalf("detail = %#v", detail)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("get command expectations: %v", err)
	}
}

func TestRetryCommandCopiesFailedTargets(t *testing.T) {
	t.Parallel()

	store, mock := newMockStore(t)
	sourceID := "task-old"
	task := core.CommandTask{ID: "task-new", CreatedBy: "owner-1", RetriedFromID: &sourceID, CreatedAt: commandTestNow}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT shell, command, timeout_seconds FROM command_tasks WHERE id = $1`)).
		WithArgs(sourceID).
		WillReturnRows(sqlmock.NewRows([]string{"shell", "command", "timeout_seconds"}).AddRow("powershell", "hostname", 300))
	mock.ExpectQuery("SELECT node_id FROM command_executions").
		WithArgs(sourceID, core.CommandFailed, core.CommandTimedOut).
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("node-1"))
	mock.ExpectExec("INSERT INTO command_tasks").
		WithArgs("task-new", core.CommandShellPowerShell, "hostname", 300, "owner-1", sourceID, commandTestNow).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO command_executions").
		WithArgs(sqlmock.AnyArg(), "task-new", "node-1", core.CommandQueued).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	detail, err := store.RetryCommand(context.Background(), task, sourceID)
	if err != nil {
		t.Fatalf("RetryCommand returned error: %v", err)
	}
	if detail.Task.Shell != core.CommandShellPowerShell || detail.Task.Command != "hostname" || detail.Task.TargetCount != 1 {
		t.Fatalf("retried detail = %#v", detail)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("retry command expectations: %v", err)
	}
}

func TestClaimCommandAuthenticatesDeviceAndLeasesOldestQueuedExecution(t *testing.T) {
	t.Parallel()

	store, mock := newMockStore(t)
	leaseExpiresAt := commandTestNow.Add(35 * time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT c.node_id").
		WithArgs("credential-hash").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("node-1"))
	mock.ExpectExec("UPDATE command_executions").
		WithArgs("node-1", core.CommandRunning, commandTestNow, core.CommandFailed, commandTestNow, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE command_executions").
		WithArgs("node-1", core.CommandLeased, commandTestNow, core.CommandQueued).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT e.id, t.id, t.shell, t.command, t.timeout_seconds").
		WithArgs("node-1", core.CommandQueued).
		WillReturnRows(sqlmock.NewRows([]string{"execution_id", "task_id", "shell", "command", "timeout_seconds"}).
			AddRow("execution-1", "task-1", "powershell", "hostname", 300))
	mock.ExpectExec("UPDATE command_executions").
		WithArgs("execution-1", "node-1", core.CommandQueued, core.CommandLeased, "lease-hash", leaseExpiresAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	claim, found, err := store.ClaimCommand(context.Background(), "credential-hash", "lease-hash", commandTestNow, 35*time.Minute)
	if err != nil || !found {
		t.Fatalf("ClaimCommand = (%#v, %v, %v)", claim, found, err)
	}
	if claim.ExecutionID != "execution-1" || claim.TaskID != "task-1" || claim.Shell != core.CommandShellPowerShell || !claim.LeaseExpiresAt.Equal(leaseExpiresAt) {
		t.Fatalf("claim = %#v", claim)
	}
	if claim.LeaseToken != "" {
		t.Fatal("Store must never return a plain lease token")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("claim command expectations: %v", err)
	}
}

func TestClaimCommandReturnsNoWorkAfterCommittingRecovery(t *testing.T) {
	t.Parallel()

	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT c.node_id").
		WithArgs("credential-hash").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("node-1"))
	mock.ExpectExec("UPDATE command_executions").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE command_executions").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT e.id, t.id, t.shell, t.command, t.timeout_seconds").
		WithArgs("node-1", core.CommandQueued).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectCommit()

	claim, found, err := store.ClaimCommand(context.Background(), "credential-hash", "lease-hash", commandTestNow, 35*time.Minute)
	if err != nil || found || claim.ExecutionID != "" {
		t.Fatalf("ClaimCommand no work = (%#v, %v, %v)", claim, found, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("no work expectations: %v", err)
	}
}

func TestStartCommandRequiresActiveCredentialAndLease(t *testing.T) {
	t.Parallel()

	store, mock := newMockStore(t)
	mock.ExpectQuery("SELECT c.node_id").
		WithArgs("credential-hash").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("node-1"))
	mock.ExpectExec("UPDATE command_executions").
		WithArgs("execution-1", "node-1", "lease-hash", commandTestNow, core.CommandLeased, core.CommandRunning).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.StartCommand(context.Background(), "credential-hash", "execution-1", "lease-hash", commandTestNow); err != nil {
		t.Fatalf("StartCommand returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("start command expectations: %v", err)
	}
}

func TestCompleteCommandStoresTerminalResult(t *testing.T) {
	t.Parallel()

	store, mock := newMockStore(t)
	exitCode := 7
	completion := core.CommandCompletion{
		ExecutionID: "execution-1", Status: core.CommandFailed, ExitCode: &exitCode,
		Output: "failed", ErrorMessage: "process exited with code 7", DurationMS: 1200,
	}
	mock.ExpectQuery("SELECT c.node_id").
		WithArgs("credential-hash").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("node-1"))
	mock.ExpectExec("UPDATE command_executions").
		WithArgs(
			"execution-1", "node-1", "lease-hash", commandTestNow, core.CommandLeased, core.CommandRunning,
			core.CommandFailed, exitCode, "failed", false, "process exited with code 7", int64(1200), commandTestNow,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.CompleteCommand(context.Background(), "credential-hash", "lease-hash", completion, commandTestNow); err != nil {
		t.Fatalf("CompleteCommand returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("complete command expectations: %v", err)
	}
}

func TestCompleteCommandAcceptsIdenticalTerminalRetry(t *testing.T) {
	t.Parallel()

	store, mock := newMockStore(t)
	exitCode := 0
	completion := core.CommandCompletion{
		ExecutionID: "execution-1", Status: core.CommandSucceeded, ExitCode: &exitCode,
		Output: "ok", DurationMS: 50,
	}
	mock.ExpectQuery("SELECT c.node_id").
		WithArgs("credential-hash").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("node-1"))
	mock.ExpectExec("UPDATE command_executions").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT status, exit_code, output, output_truncated, error_message, duration_ms").
		WithArgs("execution-1", "node-1", "lease-hash").
		WillReturnRows(sqlmock.NewRows([]string{"status", "exit_code", "output", "output_truncated", "error_message", "duration_ms"}).
			AddRow(core.CommandSucceeded, exitCode, "ok", false, "", int64(50)))

	if err := store.CompleteCommand(context.Background(), "credential-hash", "lease-hash", completion, commandTestNow); err != nil {
		t.Fatalf("CompleteCommand retry returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("complete retry expectations: %v", err)
	}
}

func TestCompleteCommandRejectsNonTerminalStatusBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()

	store, mock := newMockStore(t)
	err := store.CompleteCommand(context.Background(), "credential-hash", "lease-hash", core.CommandCompletion{
		ExecutionID: "execution-1", Status: core.CommandRunning,
	}, commandTestNow)
	if err != core.ErrConflict {
		t.Fatalf("CompleteCommand error = %v, want %v", err, core.ErrConflict)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected database access: %v", err)
	}
}
