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

func newMockStore(t *testing.T) (*Store, sqlmock.Sqlmock) {
	t.Helper()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db), mock
}

func TestMigrateExecutesEmbeddedSchema(t *testing.T) {
	t.Parallel()

	store, mock := newMockStore(t)
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS owners").WillReturnResult(sqlmock.NewResult(0, 0))

	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("migration expectations: %v", err)
	}
}

func TestEnrollNodeRejectsExpiredEnrollmentWithoutMutation(t *testing.T) {
	t.Parallel()

	store, mock := newMockStore(t)
	now := time.Date(2026, 7, 26, 2, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT group_id, expires_at, max_uses, uses FROM enrollments WHERE token_hash = $1 FOR UPDATE`)).
		WithArgs("expired-hash").
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "expires_at", "max_uses", "uses"}).
			AddRow("group-1", now.Add(-time.Minute), 1, 0))
	mock.ExpectRollback()

	_, err := store.EnrollNode(context.Background(), "expired-hash", "device-hash", core.EnrollRequest{
		Hostname: "office-pc",
		Type:     "windows",
		Version:  "0.1.0",
	}, now)
	if err != core.ErrEnrollmentExpired {
		t.Fatalf("EnrollNode error = %v, want %v", err, core.ErrEnrollmentExpired)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("transaction expectations: %v", err)
	}
}

func TestEnrollNodeCreatesNodeCredentialAndEventAtomically(t *testing.T) {
	t.Parallel()

	store, mock := newMockStore(t)
	now := time.Date(2026, 7, 26, 2, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT group_id, expires_at, max_uses, uses FROM enrollments WHERE token_hash = $1 FOR UPDATE`)).
		WithArgs("valid-hash").
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "expires_at", "max_uses", "uses"}).
			AddRow("group-1", now.Add(time.Hour), 2, 0))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE enrollments SET uses = uses + 1 WHERE token_hash = $1`)).
		WithArgs("valid-hash").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO nodes").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO node_credentials").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO events").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	node, err := store.EnrollNode(context.Background(), "valid-hash", "device-hash", core.EnrollRequest{
		Hostname:  "office-pc",
		Type:      "windows",
		Version:   "0.1.0",
		MachineID: "machine-1",
	}, now)
	if err != nil {
		t.Fatalf("EnrollNode returned error: %v", err)
	}
	if node.ID == "" || node.GroupID != "group-1" || node.Name != "office-pc" {
		t.Fatalf("node = %#v, want generated node in group-1", node)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("transaction expectations: %v", err)
	}
}

var _ *sql.DB
