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
	CreateEnrollment(context.Context, core.Enrollment) error
	EnrollNode(context.Context, string, string, core.EnrollRequest, time.Time) (core.Node, error)
	RecordHeartbeat(context.Context, string, core.Heartbeat, time.Time) (core.Node, error)
}
