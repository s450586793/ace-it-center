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
		if err := rows.Scan(&item.ID, &item.SiteID, &item.Name, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateGroup(ctx context.Context, item core.NodeGroup) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO node_groups (id, site_id, name, created_at) VALUES ($1, $2, $3, $4)`,
		item.ID, item.SiteID, item.Name, item.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create group: %w", err)
	}
	return nil
}

func (s *Store) ListNodes(ctx context.Context) ([]core.Node, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, group_id, name, type, agent_version, os_name, os_version, ip_address,
		       cpu_percent, memory_percent, disk_percent, last_seen_at, created_at
		FROM nodes ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	defer rows.Close()
	items := make([]core.Node, 0)
	for rows.Next() {
		var item core.Node
		if err := rows.Scan(
			&item.ID, &item.GroupID, &item.Name, &item.Type, &item.AgentVersion,
			&item.OSName, &item.OSVersion, &item.IPAddress, &item.CPUPercent,
			&item.MemoryPercent, &item.DiskPercent, &item.LastSeenAt, &item.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan node: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
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
	var node core.Node
	err := s.db.QueryRowContext(ctx, `
		UPDATE nodes n SET
			name = COALESCE(NULLIF($2, ''), n.name),
			agent_version = $3,
			os_name = $4,
			os_version = $5,
			ip_address = $6,
			cpu_percent = $7,
			memory_percent = $8,
			disk_percent = $9,
			last_seen_at = $10
		FROM node_credentials c
		WHERE c.node_id = n.id AND c.token_hash = $1 AND c.revoked_at IS NULL
		RETURNING n.id, n.group_id, n.name, n.type, n.agent_version, n.os_name, n.os_version,
		          n.ip_address, n.cpu_percent, n.memory_percent, n.disk_percent, n.last_seen_at, n.created_at
	`, credentialHash, heartbeat.Hostname, heartbeat.AgentVersion, heartbeat.OSName,
		heartbeat.OSVersion, heartbeat.IPAddress, heartbeat.CPUPercent, heartbeat.MemoryPercent,
		heartbeat.DiskPercent, now).Scan(
		&node.ID, &node.GroupID, &node.Name, &node.Type, &node.AgentVersion,
		&node.OSName, &node.OSVersion, &node.IPAddress, &node.CPUPercent,
		&node.MemoryPercent, &node.DiskPercent, &node.LastSeenAt, &node.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Node{}, core.ErrUnauthorized
	}
	if err != nil {
		return core.Node{}, fmt.Errorf("record heartbeat: %w", err)
	}
	return node, nil
}
