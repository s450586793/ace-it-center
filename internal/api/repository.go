package api

import (
	"context"
	"time"

	"aceitcenter.local/platform/internal/core"
)

type Repository interface {
	IsSetup(context.Context) (bool, error)
	CreateOwner(context.Context, core.Owner) error
	OwnerByUsername(context.Context, string) (core.Owner, error)
	CreateSession(context.Context, core.Session) error
	OwnerBySessionHash(context.Context, string, time.Time) (core.Owner, error)
	DeleteSession(context.Context, string) error
	ListOrganizations(context.Context) ([]core.Organization, error)
	CreateOrganization(context.Context, core.Organization) error
	ListSites(context.Context) ([]core.Site, error)
	CreateSite(context.Context, core.Site) error
	ListGroups(context.Context) ([]core.NodeGroup, error)
	CreateGroup(context.Context, core.NodeGroup) error
	ListNodes(context.Context) ([]core.Node, error)
	UpdateNodeRemark(context.Context, string, string) (core.Node, error)
	RecordAgentLogs(context.Context, string, core.AgentLogUpload, time.Time) (core.AgentLogSnapshot, error)
	GetAgentLogs(context.Context, string) (core.AgentLogSnapshot, error)
	ListNetworkHistory(context.Context, string, time.Time, time.Duration) ([]core.NetworkHistoryPoint, error)
	ListNetworkSummary(context.Context, time.Time) ([]core.NetworkSummaryItem, error)
	CreateEnrollment(context.Context, core.Enrollment) error
	EnrollNode(context.Context, string, string, core.EnrollRequest, time.Time) (core.Node, error)
	CreatePairingRequest(context.Context, core.PairingRequest, time.Time) (core.PairingRequest, error)
	GetPairingRequest(context.Context, string, string, time.Time) (core.PairingRequest, error)
	ListPendingPairingRequests(context.Context, time.Time) ([]core.PairingRequest, error)
	ApprovePairingRequest(context.Context, string, string, string, time.Time) (core.Node, error)
	RejectPairingRequest(context.Context, string, time.Time) error
	RecordHeartbeat(context.Context, string, core.Heartbeat, time.Time) (core.Node, error)
}

type CommandRepository interface {
	CreateCommand(context.Context, core.CommandTask, []string) (core.CommandTaskDetail, error)
	ListCommands(context.Context, int) ([]core.CommandTask, error)
	GetCommand(context.Context, string) (core.CommandTaskDetail, error)
	RetryCommand(context.Context, core.CommandTask, string) (core.CommandTaskDetail, error)
	ClaimCommand(context.Context, string, string, time.Time, time.Duration) (core.CommandClaim, bool, error)
	StartCommand(context.Context, string, string, string, time.Time) error
	CompleteCommand(context.Context, string, string, core.CommandCompletion, time.Time) error
}
