package systemupdate

import "context"

// CommandRunner executes one fixed platform command.
type CommandRunner interface {
	Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
}
