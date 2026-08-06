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

func TestListGroupsMapsNullSiteIDToEmptyString(t *testing.T) {
	t.Parallel()

	store, mock := newMockStore(t)
	createdAt := time.Date(2026, 8, 2, 1, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, site_id, name, created_at FROM node_groups ORDER BY name`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "site_id", "name", "created_at"}).
			AddRow("group-1", nil, "1502", createdAt))

	groups, err := store.ListGroups(context.Background())
	if err != nil {
		t.Fatalf("ListGroups returned error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("len(groups) = %d, want 1", len(groups))
	}
	if groups[0].ID != "group-1" || groups[0].SiteID != "" || groups[0].Name != "1502" || !groups[0].CreatedAt.Equal(createdAt) {
		t.Fatalf("group = %#v, want group-1 with empty SiteID", groups[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("list groups expectations: %v", err)
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

func TestRecordAgentLogsUsesActiveDeviceCredentialAndUpsertsSnapshot(t *testing.T) {
	t.Parallel()

	store, mock := newMockStore(t)
	now := time.Date(2026, 8, 1, 2, 0, 0, 0, time.UTC)
	mock.ExpectQuery("INSERT INTO node_log_snapshots").
		WithArgs("device-hash", "agent tail", "update tail", now).
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "agent_log", "update_log", "captured_at"}).
			AddRow("node-1", "agent tail", "update tail", now))

	snapshot, err := store.RecordAgentLogs(context.Background(), "device-hash", core.AgentLogUpload{
		AgentLog: "agent tail", UpdateLog: "update tail",
	}, now)
	if err != nil {
		t.Fatalf("RecordAgentLogs returned error: %v", err)
	}
	if snapshot.NodeID != "node-1" || snapshot.AgentLog != "agent tail" || snapshot.UpdateLog != "update tail" || !snapshot.CapturedAt.Equal(now) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("record log expectations: %v", err)
	}
}

func TestGetAgentLogsMapsMissingSnapshotToNotFound(t *testing.T) {
	t.Parallel()

	store, mock := newMockStore(t)
	mock.ExpectQuery("SELECT node_id, agent_log, update_log, captured_at").
		WithArgs("missing-node").
		WillReturnError(sql.ErrNoRows)

	_, err := store.GetAgentLogs(context.Background(), "missing-node")
	if err != core.ErrNotFound {
		t.Fatalf("GetAgentLogs error = %v, want %v", err, core.ErrNotFound)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("get log expectations: %v", err)
	}
}

func TestPruneRetentionDataReturnsDeletedRows(t *testing.T) {
	t.Parallel()

	store, mock := newMockStore(t)
	before := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectExec("DELETE FROM network_samples").
		WithArgs(before).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec("DELETE FROM pairing_requests").
		WithArgs(before, core.PairingPending).
		WillReturnResult(sqlmock.NewResult(0, 2))

	networkDeleted, err := store.PruneNetworkSamples(context.Background(), before)
	if err != nil || networkDeleted != 3 {
		t.Fatalf("PruneNetworkSamples() = (%d, %v), want (3, nil)", networkDeleted, err)
	}
	pairingDeleted, err := store.PrunePairingRequests(context.Background(), before)
	if err != nil || pairingDeleted != 2 {
		t.Fatalf("PrunePairingRequests() = (%d, %v), want (2, nil)", pairingDeleted, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("retention expectations: %v", err)
	}
}

var _ *sql.DB
