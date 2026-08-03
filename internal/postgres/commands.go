package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"aceitcenter.local/platform/internal/core"
	"github.com/google/uuid"
)

const commandTaskSummaryColumns = `t.id, t.shell, t.command, t.timeout_seconds, t.created_by, t.retried_from_id, t.created_at,
	COUNT(e.id),
	COUNT(e.id) FILTER (WHERE e.status = 'queued'),
	COUNT(e.id) FILTER (WHERE e.status = 'leased'),
	COUNT(e.id) FILTER (WHERE e.status = 'running'),
	COUNT(e.id) FILTER (WHERE e.status = 'succeeded'),
	COUNT(e.id) FILTER (WHERE e.status = 'failed'),
	COUNT(e.id) FILTER (WHERE e.status = 'timed_out')`

func scanCommandTask(row rowScanner) (core.CommandTask, error) {
	var task core.CommandTask
	var retriedFrom sql.NullString
	err := row.Scan(
		&task.ID, &task.Shell, &task.Command, &task.TimeoutSeconds, &task.CreatedBy, &retriedFrom, &task.CreatedAt,
		&task.TargetCount, &task.Counts.Queued, &task.Counts.Leased, &task.Counts.Running,
		&task.Counts.Succeeded, &task.Counts.Failed, &task.Counts.TimedOut,
	)
	if retriedFrom.Valid {
		task.RetriedFromID = &retriedFrom.String
	}
	return task, err
}

func scanCommandExecution(row rowScanner) (core.CommandExecution, error) {
	var execution core.CommandExecution
	var startedAt, finishedAt sql.NullTime
	var exitCode, duration sql.NullInt64
	err := row.Scan(
		&execution.ID, &execution.TaskID, &execution.NodeID, &execution.NodeName,
		&execution.Status, &execution.Attempt, &startedAt, &finishedAt, &exitCode,
		&execution.Output, &execution.OutputTruncated, &execution.ErrorMessage, &duration,
	)
	if startedAt.Valid {
		execution.StartedAt = &startedAt.Time
	}
	if finishedAt.Valid {
		execution.FinishedAt = &finishedAt.Time
	}
	if exitCode.Valid {
		value := int(exitCode.Int64)
		execution.ExitCode = &value
	}
	if duration.Valid {
		execution.DurationMS = &duration.Int64
	}
	return execution, err
}

func (s *Store) CreateCommand(ctx context.Context, task core.CommandTask, nodeIDs []string) (core.CommandTaskDetail, error) {
	if err := core.ValidateCommand(task.Shell, task.Command, task.TimeoutSeconds); err != nil {
		return core.CommandTaskDetail{}, err
	}
	if task.ID == "" || task.CreatedBy == "" || task.CreatedAt.IsZero() || len(nodeIDs) == 0 || hasDuplicateStrings(nodeIDs) {
		return core.CommandTaskDetail{}, core.ErrConflict
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.CommandTaskDetail{}, fmt.Errorf("begin command creation: %w", err)
	}
	defer tx.Rollback()

	query, arguments := commandTargetValidationQuery(nodeIDs)
	var targetCount, windowsCount int
	if err := tx.QueryRowContext(ctx, query, arguments...).Scan(&targetCount, &windowsCount); err != nil {
		return core.CommandTaskDetail{}, fmt.Errorf("validate command targets: %w", err)
	}
	if targetCount != len(nodeIDs) || windowsCount != len(nodeIDs) {
		return core.CommandTaskDetail{}, core.ErrNotFound
	}

	detail, err := insertCommand(ctx, tx, task, nodeIDs)
	if err != nil {
		return core.CommandTaskDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.CommandTaskDetail{}, fmt.Errorf("commit command creation: %w", err)
	}
	return detail, nil
}

func (s *Store) ListCommands(ctx context.Context, limit int) ([]core.CommandTask, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+commandTaskSummaryColumns+`
		FROM command_tasks t
		LEFT JOIN command_executions e ON e.task_id = t.id
		GROUP BY t.id
		ORDER BY t.created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list commands: %w", err)
	}
	defer rows.Close()

	items := make([]core.CommandTask, 0)
	for rows.Next() {
		item, err := scanCommandTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan command task: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate command tasks: %w", err)
	}
	return items, nil
}

func (s *Store) GetCommand(ctx context.Context, id string) (core.CommandTaskDetail, error) {
	task, err := scanCommandTask(s.db.QueryRowContext(ctx, `
		SELECT `+commandTaskSummaryColumns+`
		FROM command_tasks t
		LEFT JOIN command_executions e ON e.task_id = t.id
		WHERE t.id = $1
		GROUP BY t.id
	`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return core.CommandTaskDetail{}, core.ErrNotFound
	}
	if err != nil {
		return core.CommandTaskDetail{}, fmt.Errorf("find command: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.task_id, e.node_id, n.name, e.status, e.attempt,
			e.started_at, e.finished_at, e.exit_code, e.output, e.output_truncated,
			e.error_message, e.duration_ms
		FROM command_executions e
		JOIN nodes n ON n.id = e.node_id
		WHERE e.task_id = $1
		ORDER BY n.name, e.id
	`, id)
	if err != nil {
		return core.CommandTaskDetail{}, fmt.Errorf("list command executions: %w", err)
	}
	defer rows.Close()

	detail := core.CommandTaskDetail{Task: task, Executions: make([]core.CommandExecution, 0)}
	for rows.Next() {
		execution, err := scanCommandExecution(rows)
		if err != nil {
			return core.CommandTaskDetail{}, fmt.Errorf("scan command execution: %w", err)
		}
		detail.Executions = append(detail.Executions, execution)
	}
	if err := rows.Err(); err != nil {
		return core.CommandTaskDetail{}, fmt.Errorf("iterate command executions: %w", err)
	}
	return detail, nil
}

func (s *Store) RetryCommand(ctx context.Context, task core.CommandTask, sourceID string) (core.CommandTaskDetail, error) {
	if task.ID == "" || task.CreatedBy == "" || task.CreatedAt.IsZero() || sourceID == "" {
		return core.CommandTaskDetail{}, core.ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.CommandTaskDetail{}, fmt.Errorf("begin command retry: %w", err)
	}
	defer tx.Rollback()

	if err := tx.QueryRowContext(ctx, `SELECT shell, command, timeout_seconds FROM command_tasks WHERE id = $1`, sourceID).
		Scan(&task.Shell, &task.Command, &task.TimeoutSeconds); errors.Is(err, sql.ErrNoRows) {
		return core.CommandTaskDetail{}, core.ErrNotFound
	} else if err != nil {
		return core.CommandTaskDetail{}, fmt.Errorf("load source command: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT node_id FROM command_executions
		WHERE task_id = $1 AND status IN ($2, $3)
		ORDER BY node_id
	`, sourceID, core.CommandFailed, core.CommandTimedOut)
	if err != nil {
		return core.CommandTaskDetail{}, fmt.Errorf("list retry targets: %w", err)
	}
	var nodeIDs []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			rows.Close()
			return core.CommandTaskDetail{}, fmt.Errorf("scan retry target: %w", err)
		}
		nodeIDs = append(nodeIDs, nodeID)
	}
	if err := rows.Close(); err != nil {
		return core.CommandTaskDetail{}, fmt.Errorf("close retry targets: %w", err)
	}
	if err := rows.Err(); err != nil {
		return core.CommandTaskDetail{}, fmt.Errorf("iterate retry targets: %w", err)
	}
	if len(nodeIDs) == 0 {
		return core.CommandTaskDetail{}, core.ErrConflict
	}
	task.RetriedFromID = &sourceID
	detail, err := insertCommand(ctx, tx, task, nodeIDs)
	if err != nil {
		return core.CommandTaskDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.CommandTaskDetail{}, fmt.Errorf("commit command retry: %w", err)
	}
	return detail, nil
}

func insertCommand(ctx context.Context, tx *sql.Tx, task core.CommandTask, nodeIDs []string) (core.CommandTaskDetail, error) {
	var retriedFrom any
	if task.RetriedFromID != nil {
		retriedFrom = *task.RetriedFromID
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO command_tasks (id, shell, command, timeout_seconds, created_by, retried_from_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, task.ID, task.Shell, task.Command, task.TimeoutSeconds, task.CreatedBy, retriedFrom, task.CreatedAt); err != nil {
		return core.CommandTaskDetail{}, fmt.Errorf("create command task: %w", err)
	}

	query, arguments, executions := commandExecutionInsert(task.ID, nodeIDs)
	if _, err := tx.ExecContext(ctx, query, arguments...); err != nil {
		return core.CommandTaskDetail{}, fmt.Errorf("create command executions: %w", err)
	}
	task.TargetCount = len(executions)
	task.Counts = core.CommandStatusCounts{Queued: len(executions)}
	return core.CommandTaskDetail{Task: task, Executions: executions}, nil
}

func commandTargetValidationQuery(nodeIDs []string) (string, []any) {
	placeholders := make([]string, len(nodeIDs))
	arguments := make([]any, len(nodeIDs))
	for index, nodeID := range nodeIDs {
		placeholders[index] = fmt.Sprintf("$%d", index+1)
		arguments[index] = nodeID
	}
	return `SELECT COUNT(*), COUNT(*) FILTER (WHERE type = 'windows') FROM nodes WHERE id IN (` + strings.Join(placeholders, ", ") + `)`, arguments
}

func commandExecutionInsert(taskID string, nodeIDs []string) (string, []any, []core.CommandExecution) {
	values := make([]string, len(nodeIDs))
	arguments := make([]any, 0, len(nodeIDs)*4)
	executions := make([]core.CommandExecution, 0, len(nodeIDs))
	for index, nodeID := range nodeIDs {
		executionID := uuid.NewString()
		base := index*4 + 1
		values[index] = fmt.Sprintf("($%d, $%d, $%d, $%d)", base, base+1, base+2, base+3)
		arguments = append(arguments, executionID, taskID, nodeID, core.CommandQueued)
		executions = append(executions, core.CommandExecution{
			ID: executionID, TaskID: taskID, NodeID: nodeID, Status: core.CommandQueued,
		})
	}
	return `INSERT INTO command_executions (id, task_id, node_id, status) VALUES ` + strings.Join(values, ", "), arguments, executions
}

func hasDuplicateStrings(items []string) bool {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item == "" {
			return true
		}
		if _, exists := seen[item]; exists {
			return true
		}
		seen[item] = struct{}{}
	}
	return false
}

type commandQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func commandCredentialNodeID(ctx context.Context, queryer commandQueryRower, credentialHash string) (string, error) {
	var nodeID string
	err := queryer.QueryRowContext(ctx, `
		SELECT c.node_id
		FROM node_credentials c
		JOIN nodes n ON n.id = c.node_id
		WHERE c.token_hash = $1 AND c.revoked_at IS NULL AND n.type = 'windows'
	`, credentialHash).Scan(&nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", core.ErrUnauthorized
	}
	if err != nil {
		return "", fmt.Errorf("authenticate command device: %w", err)
	}
	return nodeID, nil
}

func (s *Store) ClaimCommand(
	ctx context.Context,
	credentialHash string,
	leaseHash string,
	now time.Time,
	leaseDuration time.Duration,
) (core.CommandClaim, bool, error) {
	if credentialHash == "" || leaseHash == "" || now.IsZero() || leaseDuration <= 0 {
		return core.CommandClaim{}, false, core.ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.CommandClaim{}, false, fmt.Errorf("begin command claim: %w", err)
	}
	defer tx.Rollback()

	nodeID, err := commandCredentialNodeID(ctx, tx, credentialHash)
	if err != nil {
		return core.CommandClaim{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE command_executions
		SET status = $4, finished_at = $5, error_message = $6
		WHERE node_id = $1 AND status = $2 AND lease_expires_at <= $3
	`, nodeID, core.CommandRunning, now, core.CommandFailed, now, "Agent stopped before reporting the command result"); err != nil {
		return core.CommandClaim{}, false, fmt.Errorf("expire running commands: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE command_executions
		SET status = $4, lease_token_hash = NULL, lease_expires_at = NULL
		WHERE node_id = $1 AND status = $2 AND lease_expires_at <= $3
	`, nodeID, core.CommandLeased, now, core.CommandQueued); err != nil {
		return core.CommandClaim{}, false, fmt.Errorf("recover leased commands: %w", err)
	}

	var claim core.CommandClaim
	err = tx.QueryRowContext(ctx, `
		SELECT e.id, t.id, t.shell, t.command, t.timeout_seconds
		FROM command_executions e
		JOIN command_tasks t ON t.id = e.task_id
		WHERE e.node_id = $1 AND e.status = $2
		ORDER BY t.created_at, e.id
		FOR UPDATE OF e SKIP LOCKED
		LIMIT 1
	`, nodeID, core.CommandQueued).Scan(
		&claim.ExecutionID, &claim.TaskID, &claim.Shell, &claim.Command, &claim.TimeoutSeconds,
	)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return core.CommandClaim{}, false, fmt.Errorf("commit empty command claim: %w", err)
		}
		return core.CommandClaim{}, false, nil
	}
	if err != nil {
		return core.CommandClaim{}, false, fmt.Errorf("select command claim: %w", err)
	}
	claim.LeaseExpiresAt = now.Add(leaseDuration)
	result, err := tx.ExecContext(ctx, `
		UPDATE command_executions
		SET status = $4, attempt = attempt + 1, lease_token_hash = $5, lease_expires_at = $6
		WHERE id = $1 AND node_id = $2 AND status = $3
	`, claim.ExecutionID, nodeID, core.CommandQueued, core.CommandLeased, leaseHash, claim.LeaseExpiresAt)
	if err != nil {
		return core.CommandClaim{}, false, fmt.Errorf("lease command: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return core.CommandClaim{}, false, fmt.Errorf("count leased command: %w", err)
	}
	if count != 1 {
		return core.CommandClaim{}, false, core.ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return core.CommandClaim{}, false, fmt.Errorf("commit command claim: %w", err)
	}
	return claim, true, nil
}

func (s *Store) StartCommand(ctx context.Context, credentialHash, executionID, leaseHash string, now time.Time) error {
	if credentialHash == "" || executionID == "" || leaseHash == "" || now.IsZero() {
		return core.ErrConflict
	}
	nodeID, err := commandCredentialNodeID(ctx, s.db, credentialHash)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE command_executions
		SET status = $6, started_at = COALESCE(started_at, $4)
		WHERE id = $1 AND node_id = $2 AND lease_token_hash = $3 AND lease_expires_at > $4
			AND status IN ($5, $6)
	`, executionID, nodeID, leaseHash, now, core.CommandLeased, core.CommandRunning)
	if err != nil {
		return fmt.Errorf("start command: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count started command: %w", err)
	}
	if count != 1 {
		return core.ErrConflict
	}
	return nil
}

func (s *Store) CompleteCommand(
	ctx context.Context,
	credentialHash string,
	leaseHash string,
	completion core.CommandCompletion,
	now time.Time,
) error {
	if !validCommandCompletion(completion) || credentialHash == "" || leaseHash == "" || now.IsZero() {
		return core.ErrConflict
	}
	nodeID, err := commandCredentialNodeID(ctx, s.db, credentialHash)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE command_executions
		SET status = $7, exit_code = $8, output = $9, output_truncated = $10,
			error_message = $11, duration_ms = $12, finished_at = $13
		WHERE id = $1 AND node_id = $2 AND lease_token_hash = $3 AND lease_expires_at > $4
			AND status IN ($5, $6)
	`, completion.ExecutionID, nodeID, leaseHash, now, core.CommandLeased, core.CommandRunning,
		completion.Status, completion.ExitCode, completion.Output, completion.OutputTruncated,
		completion.ErrorMessage, completion.DurationMS, now)
	if err != nil {
		return fmt.Errorf("complete command: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count completed command: %w", err)
	}
	if count == 1 {
		return nil
	}

	var storedStatus core.CommandStatus
	var storedExitCode, storedDuration sql.NullInt64
	var storedOutput, storedError string
	var storedTruncated bool
	err = s.db.QueryRowContext(ctx, `
		SELECT status, exit_code, output, output_truncated, error_message, duration_ms
		FROM command_executions
		WHERE id = $1 AND node_id = $2 AND lease_token_hash = $3
	`, completion.ExecutionID, nodeID, leaseHash).Scan(
		&storedStatus, &storedExitCode, &storedOutput, &storedTruncated, &storedError, &storedDuration,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("load completed command: %w", err)
	}
	if sameCommandCompletion(completion, storedStatus, storedExitCode, storedOutput, storedTruncated, storedError, storedDuration) {
		return nil
	}
	return core.ErrConflict
}

func validCommandCompletion(completion core.CommandCompletion) bool {
	return completion.ExecutionID != "" && completion.Status.Terminal() && completion.DurationMS >= 0 &&
		utf8.ValidString(completion.Output) && len(completion.Output) <= core.MaxCommandOutputBytes &&
		utf8.ValidString(completion.ErrorMessage) && len(completion.ErrorMessage) <= 512
}

func sameCommandCompletion(
	completion core.CommandCompletion,
	status core.CommandStatus,
	exitCode sql.NullInt64,
	output string,
	truncated bool,
	errorMessage string,
	duration sql.NullInt64,
) bool {
	if status != completion.Status || output != completion.Output || truncated != completion.OutputTruncated ||
		errorMessage != completion.ErrorMessage || !duration.Valid || duration.Int64 != completion.DurationMS {
		return false
	}
	if completion.ExitCode == nil {
		return !exitCode.Valid
	}
	return exitCode.Valid && exitCode.Int64 == int64(*completion.ExitCode)
}
