package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"aceitcenter.local/platform/internal/core"
	"github.com/google/uuid"
)

//go:embed schema.sql
var schemaSQL string

type Store struct {
	db *sql.DB
}

type rowScanner interface {
	Scan(...any) error
}

const nodeColumns = `nodes.id, nodes.group_id, nodes.remark, nodes.name, nodes.type, nodes.agent_version,
		nodes.os_name, nodes.os_version, nodes.ip_address, nodes.cpu_percent, nodes.memory_percent,
		nodes.disk_percent, nodes.network_metrics_available, nodes.network_upload_mb_s,
		nodes.network_download_mb_s, nodes.network_usage_available, nodes.network_usage_day,
		nodes.network_today_upload_bytes, nodes.network_today_download_bytes,
		nodes.network_month_upload_bytes, nodes.network_month_download_bytes,
		nodes.last_seen_at, nodes.created_at`

func scanNode(row rowScanner) (core.Node, error) {
	var node core.Node
	err := row.Scan(
		&node.ID, &node.GroupID, &node.Remark, &node.Name, &node.Type, &node.AgentVersion,
		&node.OSName, &node.OSVersion, &node.IPAddress, &node.CPUPercent, &node.MemoryPercent,
		&node.DiskPercent, &node.NetworkMetricsAvailable, &node.NetworkUploadMBPerSecond,
		&node.NetworkDownloadMBPerSecond, &node.NetworkUsageAvailable, &node.NetworkUsageDay,
		&node.NetworkTodayUploadBytes, &node.NetworkTodayDownloadBytes,
		&node.NetworkMonthUploadBytes, &node.NetworkMonthDownloadBytes,
		&node.LastSeenAt, &node.CreatedAt,
	)
	return node, err
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply database schema: %w", err)
	}
	return nil
}

func (s *Store) IsSetup(ctx context.Context) (bool, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM owners)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("check owner setup: %w", err)
	}
	return exists, nil
}

func (s *Store) CreateOwner(ctx context.Context, owner core.Owner) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO owners (id, username, password_hash, created_at) VALUES ($1, $2, $3, $4)`,
		owner.ID, owner.Username, owner.PasswordHash, owner.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create owner: %w", err)
	}
	return nil
}

func (s *Store) OwnerByUsername(ctx context.Context, username string) (core.Owner, error) {
	var owner core.Owner
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, created_at FROM owners WHERE username = $1`,
		username,
	).Scan(&owner.ID, &owner.Username, &owner.PasswordHash, &owner.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Owner{}, core.ErrNotFound
	}
	if err != nil {
		return core.Owner{}, fmt.Errorf("find owner: %w", err)
	}
	return owner, nil
}

func (s *Store) CreateSession(ctx context.Context, session core.Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, owner_id, token_hash, expires_at, created_at) VALUES ($1, $2, $3, $4, $5)`,
		session.ID, session.OwnerID, session.TokenHash, session.ExpiresAt, session.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (s *Store) OwnerBySessionHash(ctx context.Context, hash string, now time.Time) (core.Owner, error) {
	var owner core.Owner
	err := s.db.QueryRowContext(ctx, `
		SELECT o.id, o.username, o.password_hash, o.created_at
		FROM sessions s
		JOIN owners o ON o.id = s.owner_id
		WHERE s.token_hash = $1 AND s.expires_at > $2
	`, hash, now).Scan(&owner.ID, &owner.Username, &owner.PasswordHash, &owner.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Owner{}, core.ErrUnauthorized
	}
	if err != nil {
		return core.Owner{}, fmt.Errorf("find session owner: %w", err)
	}
	return owner, nil
}

func (s *Store) DeleteSession(ctx context.Context, hash string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = $1`, hash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func (s *Store) ListOrganizations(ctx context.Context) ([]core.Organization, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, created_at FROM organizations ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	defer rows.Close()

	items := make([]core.Organization, 0)
	for rows.Next() {
		var item core.Organization
		if err := rows.Scan(&item.ID, &item.Name, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan organization: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateOrganization(ctx context.Context, item core.Organization) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO organizations (id, name, created_at) VALUES ($1, $2, $3)`,
		item.ID, item.Name, item.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create organization: %w", err)
	}
	return nil
}

func (s *Store) ListSites(ctx context.Context) ([]core.Site, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, organization_id, name, created_at FROM sites ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list sites: %w", err)
	}
	defer rows.Close()
	items := make([]core.Site, 0)
	for rows.Next() {
		var item core.Site
		if err := rows.Scan(&item.ID, &item.OrganizationID, &item.Name, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan site: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateSite(ctx context.Context, item core.Site) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sites (id, organization_id, name, created_at) VALUES ($1, $2, $3, $4)`,
		item.ID, item.OrganizationID, item.Name, item.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create site: %w", err)
	}
	return nil
}

func (s *Store) ListGroups(ctx context.Context) ([]core.NodeGroup, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, site_id, name, created_at FROM node_groups ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	defer rows.Close()
	items := make([]core.NodeGroup, 0)
	for rows.Next() {
		var item core.NodeGroup
		var siteID sql.NullString
		if err := rows.Scan(&item.ID, &siteID, &item.Name, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		item.SiteID = siteID.String
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateGroup(ctx context.Context, item core.NodeGroup) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO node_groups (id, site_id, name, created_at) VALUES ($1, NULLIF($2, ''), $3, $4)`,
		item.ID, item.SiteID, item.Name, item.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create group: %w", err)
	}
	return nil
}

func (s *Store) ListNodes(ctx context.Context) ([]core.Node, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+nodeColumns+`
		FROM nodes ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	defer rows.Close()
	items := make([]core.Node, 0)
	for rows.Next() {
		item, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("scan node: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpdateNodeRemark(ctx context.Context, nodeID, remark string) (core.Node, error) {
	node, err := scanNode(s.db.QueryRowContext(ctx, `
		UPDATE nodes SET remark = $2
		WHERE id = $1
		RETURNING `+nodeColumns,
		nodeID, remark,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return core.Node{}, core.ErrNotFound
	}
	if err != nil {
		return core.Node{}, fmt.Errorf("update node remark: %w", err)
	}
	return node, nil
}

func (s *Store) ListNetworkHistory(
	ctx context.Context,
	nodeID string,
	since time.Time,
	bucket time.Duration,
) ([]core.NetworkHistoryPoint, error) {
	if bucket <= 0 {
		return nil, fmt.Errorf("network history bucket must be positive")
	}
	if err := s.requireNode(ctx, nodeID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			to_timestamp(FLOOR(EXTRACT(EPOCH FROM captured_at) / $3) * $3),
			AVG(upload_mb_s), AVG(download_mb_s), MAX(upload_mb_s), MAX(download_mb_s)
		FROM network_samples
		WHERE node_id = $1 AND captured_at >= $2
		GROUP BY 1
		ORDER BY 1
	`, nodeID, since, bucket.Seconds())
	if err != nil {
		return nil, fmt.Errorf("list network history: %w", err)
	}
	defer rows.Close()
	points := make([]core.NetworkHistoryPoint, 0)
	for rows.Next() {
		var point core.NetworkHistoryPoint
		if err := rows.Scan(
			&point.CapturedAt, &point.UploadAverageMBPerSecond, &point.DownloadAverageMBPerSecond,
			&point.UploadPeakMBPerSecond, &point.DownloadPeakMBPerSecond,
		); err != nil {
			return nil, fmt.Errorf("scan network history: %w", err)
		}
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate network history: %w", err)
	}
	return points, nil
}

func (s *Store) ListNetworkSummary(ctx context.Context, since time.Time) ([]core.NetworkSummaryItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT node_id, AVG(upload_mb_s), AVG(download_mb_s), MAX(upload_mb_s), MAX(download_mb_s)
		FROM network_samples
		WHERE captured_at >= $1
		GROUP BY node_id
	`, since)
	if err != nil {
		return nil, fmt.Errorf("list network summary: %w", err)
	}
	defer rows.Close()
	items := make([]core.NetworkSummaryItem, 0)
	for rows.Next() {
		var item core.NetworkSummaryItem
		if err := rows.Scan(
			&item.NodeID, &item.UploadAverageMBPerSecond, &item.DownloadAverageMBPerSecond,
			&item.UploadPeakMBPerSecond, &item.DownloadPeakMBPerSecond,
		); err != nil {
			return nil, fmt.Errorf("scan network summary: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate network summary: %w", err)
	}
	return items, nil
}

func (s *Store) PruneNetworkSamples(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM network_samples WHERE captured_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("prune network samples: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count pruned network samples: %w", err)
	}
	return count, nil
}

func (s *Store) requireNode(ctx context.Context, nodeID string) error {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM nodes WHERE id = $1)`, nodeID).Scan(&exists); err != nil {
		return fmt.Errorf("check node: %w", err)
	}
	if !exists {
		return core.ErrNotFound
	}
	return nil
}

func (s *Store) CreateEnrollment(ctx context.Context, item core.Enrollment) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO enrollments (id, group_id, token_hash, expires_at, max_uses, uses, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, item.ID, item.GroupID, item.TokenHash, item.ExpiresAt, item.MaxUses, item.Uses, item.CreatedAt)
	if err != nil {
		return fmt.Errorf("create enrollment: %w", err)
	}
	return nil
}

func (s *Store) EnrollNode(
	ctx context.Context,
	enrollmentHash string,
	deviceHash string,
	request core.EnrollRequest,
	now time.Time,
) (core.Node, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Node{}, fmt.Errorf("begin enrollment transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var groupID string
	var expiresAt time.Time
	var maxUses, uses int
	err = tx.QueryRowContext(ctx,
		`SELECT group_id, expires_at, max_uses, uses FROM enrollments WHERE token_hash = $1 FOR UPDATE`,
		enrollmentHash,
	).Scan(&groupID, &expiresAt, &maxUses, &uses)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Node{}, core.ErrUnauthorized
	}
	if err != nil {
		return core.Node{}, fmt.Errorf("load enrollment: %w", err)
	}
	if !expiresAt.After(now) || uses >= maxUses {
		return core.Node{}, core.ErrEnrollmentExpired
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE enrollments SET uses = uses + 1 WHERE token_hash = $1`,
		enrollmentHash,
	); err != nil {
		return core.Node{}, fmt.Errorf("consume enrollment: %w", err)
	}

	node := core.Node{
		ID:           uuid.NewString(),
		GroupID:      groupID,
		Name:         request.Hostname,
		Type:         request.Type,
		AgentVersion: request.Version,
		LastSeenAt:   &now,
		CreatedAt:    now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO nodes (id, group_id, name, type, machine_id, agent_version, last_seen_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, node.ID, node.GroupID, node.Name, node.Type, request.MachineID, node.AgentVersion, now, now); err != nil {
		return core.Node{}, fmt.Errorf("create node: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO node_credentials (node_id, token_hash, created_at) VALUES ($1, $2, $3)
	`, node.ID, deviceHash, now); err != nil {
		return core.Node{}, fmt.Errorf("create node credential: %w", err)
	}
	payload, _ := json.Marshal(map[string]string{"hostname": node.Name, "type": node.Type})
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO events (id, node_id, type, payload, created_at) VALUES ($1, $2, $3, $4, $5)
	`, uuid.NewString(), node.ID, "node.enrolled", payload, now); err != nil {
		return core.Node{}, fmt.Errorf("create enrollment event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return core.Node{}, fmt.Errorf("commit enrollment: %w", err)
	}
	return node, nil
}

func (s *Store) RecordHeartbeat(
	ctx context.Context,
	credentialHash string,
	heartbeat core.Heartbeat,
	now time.Time,
) (core.Node, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Node{}, fmt.Errorf("begin heartbeat transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	node, err := scanNode(tx.QueryRowContext(ctx, `
		UPDATE nodes SET
			name = COALESCE(NULLIF($2, ''), nodes.name),
			agent_version = $3,
			os_name = $4,
			os_version = $5,
			ip_address = $6,
			cpu_percent = $7,
			memory_percent = $8,
			disk_percent = $9,
			network_metrics_available = $10,
			network_upload_mb_s = $11,
			network_download_mb_s = $12,
			network_usage_available = $13,
			network_usage_day = $14,
			network_today_upload_bytes = $15,
			network_today_download_bytes = $16,
			network_month_upload_bytes = $17,
			network_month_download_bytes = $18,
			last_seen_at = $19
		FROM node_credentials c
		WHERE c.node_id = nodes.id AND c.token_hash = $1 AND c.revoked_at IS NULL
		RETURNING `+nodeColumns,
		credentialHash, heartbeat.Hostname, heartbeat.AgentVersion, heartbeat.OSName,
		heartbeat.OSVersion, heartbeat.IPAddress, heartbeat.CPUPercent, heartbeat.MemoryPercent,
		heartbeat.DiskPercent, heartbeat.NetworkMetricsAvailable, heartbeat.NetworkUploadMBPerSecond,
		heartbeat.NetworkDownloadMBPerSecond, heartbeat.NetworkUsageAvailable, heartbeat.NetworkUsageDay,
		heartbeat.NetworkTodayUploadBytes, heartbeat.NetworkTodayDownloadBytes,
		heartbeat.NetworkMonthUploadBytes, heartbeat.NetworkMonthDownloadBytes, now))
	if errors.Is(err, sql.ErrNoRows) {
		return core.Node{}, core.ErrUnauthorized
	}
	if err != nil {
		return core.Node{}, fmt.Errorf("record heartbeat: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO network_samples (node_id, captured_at, upload_mb_s, download_mb_s)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (node_id, captured_at) DO UPDATE SET
			upload_mb_s = EXCLUDED.upload_mb_s,
			download_mb_s = EXCLUDED.download_mb_s
	`, node.ID, now, heartbeat.NetworkUploadMBPerSecond, heartbeat.NetworkDownloadMBPerSecond); err != nil {
		return core.Node{}, fmt.Errorf("record network sample: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return core.Node{}, fmt.Errorf("commit heartbeat: %w", err)
	}
	return node, nil
}

func (s *Store) RecordAgentLogs(
	ctx context.Context,
	credentialHash string,
	logs core.AgentLogUpload,
	now time.Time,
) (core.AgentLogSnapshot, error) {
	var snapshot core.AgentLogSnapshot
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO node_log_snapshots (node_id, agent_log, update_log, captured_at)
		SELECT c.node_id, $2, $3, $4
		FROM node_credentials c
		WHERE c.token_hash = $1 AND c.revoked_at IS NULL
		ON CONFLICT (node_id) DO UPDATE SET
			agent_log = EXCLUDED.agent_log,
			update_log = EXCLUDED.update_log,
			captured_at = EXCLUDED.captured_at
		RETURNING node_id, agent_log, update_log, captured_at
	`, credentialHash, logs.AgentLog, logs.UpdateLog, now).Scan(
		&snapshot.NodeID, &snapshot.AgentLog, &snapshot.UpdateLog, &snapshot.CapturedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return core.AgentLogSnapshot{}, core.ErrUnauthorized
	}
	if err != nil {
		return core.AgentLogSnapshot{}, fmt.Errorf("record agent logs: %w", err)
	}
	return snapshot, nil
}

func (s *Store) GetAgentLogs(ctx context.Context, nodeID string) (core.AgentLogSnapshot, error) {
	var snapshot core.AgentLogSnapshot
	err := s.db.QueryRowContext(ctx, `
		SELECT node_id, agent_log, update_log, captured_at
		FROM node_log_snapshots
		WHERE node_id = $1
	`, nodeID).Scan(&snapshot.NodeID, &snapshot.AgentLog, &snapshot.UpdateLog, &snapshot.CapturedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.AgentLogSnapshot{}, core.ErrNotFound
	}
	if err != nil {
		return core.AgentLogSnapshot{}, fmt.Errorf("get agent logs: %w", err)
	}
	return snapshot, nil
}

func (s *Store) CreatePairingRequest(ctx context.Context, request core.PairingRequest, now time.Time) (core.PairingRequest, error) {
	request.CreatedAt = now
	request.ExpiresAt = now.Add(10 * time.Minute)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO pairing_requests (
			id, machine_id, hostname, type, agent_version, credential_hash, state, created_at, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, request.ID, request.MachineID, request.Hostname, request.Type, request.AgentVersion,
		request.CredentialHash, request.State, request.CreatedAt, request.ExpiresAt)
	if err != nil {
		return core.PairingRequest{}, fmt.Errorf("create pairing request: %w", err)
	}
	return request, nil
}

func (s *Store) GetPairingRequest(ctx context.Context, id, credentialHash string, now time.Time) (core.PairingRequest, error) {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE pairing_requests
		SET state = $2, decided_at = COALESCE(decided_at, $3)
		WHERE id = $1 AND state = $4 AND expires_at <= $3
	`, id, core.PairingExpired, now, core.PairingPending); err != nil {
		return core.PairingRequest{}, fmt.Errorf("expire pairing request: %w", err)
	}
	pairing, nodeID, err := s.pairingByIDAndCredential(ctx, id, credentialHash)
	if errors.Is(err, sql.ErrNoRows) {
		return core.PairingRequest{}, core.ErrUnauthorized
	}
	if err != nil {
		return core.PairingRequest{}, fmt.Errorf("get pairing request: %w", err)
	}
	if nodeID != "" {
		node, err := s.nodeByID(ctx, nodeID)
		if err != nil {
			return core.PairingRequest{}, fmt.Errorf("load paired node: %w", err)
		}
		pairing.ExistingNode = &node
	}
	return pairing, nil
}

func (s *Store) ListPendingPairingRequests(ctx context.Context, now time.Time) ([]core.PairingRequest, error) {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE pairing_requests
		SET state = $1, decided_at = COALESCE(decided_at, $2)
		WHERE state = $3 AND expires_at <= $2
	`, core.PairingExpired, now, core.PairingPending); err != nil {
		return nil, fmt.Errorf("expire pending pairings: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, machine_id, hostname, type, agent_version, state, group_id, created_at, expires_at, decided_at
		FROM pairing_requests
		WHERE state = $1 AND expires_at > $2
		ORDER BY created_at
	`, core.PairingPending, now)
	if err != nil {
		return nil, fmt.Errorf("list pending pairings: %w", err)
	}
	defer rows.Close()
	items := make([]core.PairingRequest, 0)
	for rows.Next() {
		item, err := scanPairing(rows)
		if err != nil {
			return nil, fmt.Errorf("scan pending pairing: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending pairings: %w", err)
	}
	return items, nil
}

func (s *Store) ApprovePairingRequest(ctx context.Context, id, groupID, remark string, now time.Time) (core.Node, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Node{}, fmt.Errorf("begin pairing approval transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var machineID, hostname, nodeType, version, credentialHash string
	var expiresAt time.Time
	var state core.PairingState
	err = tx.QueryRowContext(ctx, `
		SELECT machine_id, hostname, type, agent_version, credential_hash, state, expires_at
		FROM pairing_requests
		WHERE id = $1
		FOR UPDATE
	`, id).Scan(&machineID, &hostname, &nodeType, &version, &credentialHash, &state, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Node{}, core.ErrNotFound
	}
	if err != nil {
		return core.Node{}, fmt.Errorf("load pairing request: %w", err)
	}
	if state != core.PairingPending {
		if state == core.PairingExpired {
			return core.Node{}, core.ErrPairingExpired
		}
		return core.Node{}, core.ErrConflict
	}
	if !expiresAt.After(now) {
		if _, err := tx.ExecContext(ctx, `UPDATE pairing_requests SET state = $2, decided_at = $3 WHERE id = $1`, id, core.PairingExpired, now); err != nil {
			return core.Node{}, fmt.Errorf("expire pairing request: %w", err)
		}
		return core.Node{}, core.ErrPairingExpired
	}

	node, err := scanNode(tx.QueryRowContext(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE machine_id = $1`, machineID))
	if errors.Is(err, sql.ErrNoRows) {
		node = core.Node{
			ID:           uuid.NewString(),
			GroupID:      groupID,
			Remark:       remark,
			Name:         hostname,
			Type:         nodeType,
			AgentVersion: version,
			CreatedAt:    now,
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (id, group_id, remark, name, type, machine_id, agent_version, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, node.ID, node.GroupID, node.Remark, node.Name, node.Type, machineID, node.AgentVersion, node.CreatedAt); err != nil {
			return core.Node{}, fmt.Errorf("create paired node: %w", err)
		}
	} else if err != nil {
		return core.Node{}, fmt.Errorf("find paired node: %w", err)
	} else {
		node, err = scanNode(tx.QueryRowContext(ctx, `
			UPDATE nodes SET group_id = $2, remark = $3, name = $4, type = $5, agent_version = $6
			WHERE id = $1
			RETURNING `+nodeColumns,
			node.ID, groupID, remark, hostname, nodeType, version,
		))
		if err != nil {
			return core.Node{}, fmt.Errorf("update paired node: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO node_credentials (node_id, token_hash, created_at, revoked_at)
		VALUES ($1, $2, $3, NULL)
		ON CONFLICT (node_id) DO UPDATE SET
			token_hash = EXCLUDED.token_hash,
			created_at = EXCLUDED.created_at,
			revoked_at = NULL
	`, node.ID, credentialHash, now); err != nil {
		return core.Node{}, fmt.Errorf("store paired node credential: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE pairing_requests
		SET state = $2, node_id = $3, group_id = $4, remark = $5, decided_at = $6
		WHERE id = $1
	`, id, core.PairingApproved, node.ID, groupID, remark, now); err != nil {
		return core.Node{}, fmt.Errorf("approve pairing request: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return core.Node{}, fmt.Errorf("commit pairing approval: %w", err)
	}
	return node, nil
}

func (s *Store) RejectPairingRequest(ctx context.Context, id string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE pairing_requests
		SET state = $2, decided_at = $3
		WHERE id = $1 AND state = $4 AND expires_at > $3
	`, id, core.PairingRejected, now, core.PairingPending)
	if err != nil {
		return fmt.Errorf("reject pairing request: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count rejected pairing request: %w", err)
	}
	if count == 1 {
		return nil
	}
	var state core.PairingState
	var expiresAt time.Time
	err = s.db.QueryRowContext(ctx, `SELECT state, expires_at FROM pairing_requests WHERE id = $1`, id).Scan(&state, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load rejected pairing request: %w", err)
	}
	if state == core.PairingExpired || !expiresAt.After(now) {
		return core.ErrPairingExpired
	}
	return core.ErrConflict
}

func (s *Store) PrunePairingRequests(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM pairing_requests
		WHERE created_at < $1 AND state <> $2
	`, before, core.PairingPending)
	if err != nil {
		return 0, fmt.Errorf("prune pairing requests: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count pruned pairing requests: %w", err)
	}
	return count, nil
}

func (s *Store) pairingByIDAndCredential(ctx context.Context, id, credentialHash string) (core.PairingRequest, string, error) {
	var nodeID sql.NullString
	row := s.db.QueryRowContext(ctx, `
		SELECT id, machine_id, hostname, type, agent_version, state, group_id, created_at, expires_at, decided_at, node_id
		FROM pairing_requests
		WHERE id = $1 AND credential_hash = $2
	`, id, credentialHash)
	pairing, err := scanPairingWithNodeID(row, &nodeID)
	return pairing, nodeID.String, err
}

func scanPairing(row rowScanner) (core.PairingRequest, error) {
	return scanPairingWithNodeID(row, nil)
}

func scanPairingWithNodeID(row rowScanner, nodeID *sql.NullString) (core.PairingRequest, error) {
	var pairing core.PairingRequest
	var groupID sql.NullString
	var decidedAt sql.NullTime
	fields := []any{
		&pairing.ID, &pairing.MachineID, &pairing.Hostname, &pairing.Type, &pairing.AgentVersion,
		&pairing.State, &groupID, &pairing.CreatedAt, &pairing.ExpiresAt, &decidedAt,
	}
	if nodeID != nil {
		fields = append(fields, nodeID)
	}
	if err := row.Scan(fields...); err != nil {
		return core.PairingRequest{}, err
	}
	if groupID.Valid {
		pairing.GroupID = groupID.String
	}
	if decidedAt.Valid {
		pairing.DecidedAt = &decidedAt.Time
	}
	return pairing, nil
}

func (s *Store) nodeByID(ctx context.Context, nodeID string) (core.Node, error) {
	node, err := scanNode(s.db.QueryRowContext(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE id = $1`, nodeID))
	if errors.Is(err, sql.ErrNoRows) {
		return core.Node{}, core.ErrNotFound
	}
	if err != nil {
		return core.Node{}, fmt.Errorf("load node: %w", err)
	}
	return node, nil
}
