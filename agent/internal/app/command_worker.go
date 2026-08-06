package app

import (
	"context"
	"errors"
	"strings"
	"time"

	agentclient "aceitcenter.local/platform/agent/internal/agent"
	"aceitcenter.local/platform/internal/core"
)

const commandClaimRetryDelay = 5 * time.Second

var commandResultRetryDelays = []time.Duration{
	time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	15 * time.Second,
}

type CommandClient interface {
	ClaimCommand(context.Context, string) (core.CommandClaim, bool, error)
	StartCommand(context.Context, string, string, string) error
	CompleteCommand(context.Context, string, core.CommandCompletion) error
}

type CommandExecutor interface {
	Execute(context.Context, core.CommandClaim) core.CommandCompletion
}

type CommandWait func(context.Context, time.Duration) bool

type CommandWorkerOptions struct {
	Client    CommandClient
	Executor  CommandExecutor
	Wait      CommandWait
	Now       func() time.Time
	ErrorSink func(string)
}

type CommandWorker struct {
	options CommandWorkerOptions
}

func NewCommandWorker(options CommandWorkerOptions) *CommandWorker {
	if options.Wait == nil {
		options.Wait = waitForCommandRetry
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &CommandWorker{options: options}
}

func (w *CommandWorker) Run(ctx context.Context, config agentclient.Config) error {
	if w.options.Client == nil {
		return &ConfigurationError{Message: "command client is required"}
	}
	if w.options.Executor == nil {
		return &ConfigurationError{Message: "command executor is required"}
	}
	if config.Credential == "" {
		return &ConfigurationError{Message: "agent credential is required"}
	}

	for {
		if ctx.Err() != nil {
			return nil
		}
		claim, found, err := w.options.Client.ClaimCommand(ctx, config.Credential)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.report(err, config.Credential, "")
			if !w.options.Wait(ctx, commandClaimRetryDelay) {
				return nil
			}
			continue
		}
		if !found {
			continue
		}
		if !w.start(ctx, config.Credential, claim) {
			continue
		}

		completion := w.options.Executor.Execute(ctx, claim)
		if !completion.Status.Terminal() {
			completion.Status = core.CommandFailed
			completion.ErrorMessage = "command executor returned an invalid status"
		}
		completion.ExecutionID = claim.ExecutionID
		completion.LeaseToken = claim.LeaseToken
		w.complete(ctx, config.Credential, claim, completion)
	}
}

func (w *CommandWorker) start(ctx context.Context, credential string, claim core.CommandClaim) bool {
	for attempt := 0; ; attempt++ {
		err := w.options.Client.StartCommand(ctx, credential, claim.ExecutionID, claim.LeaseToken)
		if err == nil {
			return true
		}
		if errors.Is(err, agentclient.ErrCommandLeaseRejected) || ctx.Err() != nil || !w.options.Now().Before(claim.LeaseExpiresAt) {
			w.report(err, credential, claim.LeaseToken)
			return false
		}
		w.report(err, credential, claim.LeaseToken)
		if !w.options.Wait(ctx, commandRetryDelay(attempt)) {
			return false
		}
	}
}

func (w *CommandWorker) complete(
	ctx context.Context,
	credential string,
	claim core.CommandClaim,
	completion core.CommandCompletion,
) {
	for attempt := 0; ; attempt++ {
		err := w.options.Client.CompleteCommand(ctx, credential, completion)
		if err == nil {
			return
		}
		if errors.Is(err, agentclient.ErrCommandLeaseRejected) || ctx.Err() != nil || !w.options.Now().Before(claim.LeaseExpiresAt) {
			w.report(err, credential, claim.LeaseToken)
			return
		}
		w.report(err, credential, claim.LeaseToken)
		if !w.options.Wait(ctx, commandRetryDelay(attempt)) {
			return
		}
	}
}

func commandRetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt >= len(commandResultRetryDelays) {
		return commandResultRetryDelays[len(commandResultRetryDelays)-1]
	}
	return commandResultRetryDelays[attempt]
}

func (w *CommandWorker) report(err error, credential, leaseToken string) {
	if err == nil || w.options.ErrorSink == nil {
		return
	}
	message := sanitizeError(err, credential)
	if leaseToken != "" {
		message = strings.ReplaceAll(message, leaseToken, "[redacted]")
	}
	w.options.ErrorSink(message)
}

func waitForCommandRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
