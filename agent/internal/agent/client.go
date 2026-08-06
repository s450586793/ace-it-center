package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aceitcenter.local/platform/internal/core"
)

type Client struct {
	serverURL string
	http      *http.Client
}

type EnrollResult struct {
	Node       core.Node `json:"node"`
	Credential string    `json:"credential"`
}

var ErrPairingRejected = errors.New("pairing rejected")
var ErrCommandLeaseRejected = errors.New("command lease rejected")

const defaultPairingPollAfter = 5 * time.Second

func NewClient(serverURL string, httpClient *http.Client) (*Client, error) {
	if !isHTTPServerURL(serverURL) {
		return nil, fmt.Errorf("server URL must use HTTP or HTTPS")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{serverURL: strings.TrimRight(serverURL, "/"), http: httpClient}, nil
}

func (c *Client) StartPairing(ctx context.Context, request core.PairingCreateRequest) (core.PairingPollResult, time.Duration, error) {
	var response struct {
		core.PairingPollResult
		PollAfterSeconds int `json:"poll_after_seconds"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent/pairings", "", request, &response); err != nil {
		return core.PairingPollResult{}, 0, err
	}
	if response.ID == "" || response.ExpiresAt.IsZero() || !response.ExpiresAt.After(time.Now()) {
		return core.PairingPollResult{}, 0, fmt.Errorf("pairing response is incomplete")
	}
	pollAfter := time.Duration(response.PollAfterSeconds) * time.Second
	if pollAfter <= 0 {
		pollAfter = defaultPairingPollAfter
	}
	return response.PairingPollResult, pollAfter, nil
}

func (c *Client) PollPairing(ctx context.Context, pairingID, credential string) (core.PairingPollResult, error) {
	response, err := c.sendJSON(ctx, http.MethodGet, "/api/v1/agent/pairings/"+url.PathEscape(pairingID), credential, nil)
	if err != nil {
		return core.PairingPollResult{}, err
	}
	defer response.Body.Close()

	var result core.PairingPollResult
	if response.StatusCode == http.StatusGone {
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
			return core.PairingPollResult{}, fmt.Errorf("decode response: %w", err)
		}
		switch result.State {
		case core.PairingRejected:
			return result, ErrPairingRejected
		case core.PairingExpired:
			return result, core.ErrPairingExpired
		default:
			return core.PairingPollResult{}, fmt.Errorf("request failed (%d)", response.StatusCode)
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return core.PairingPollResult{}, fmt.Errorf("request failed (%d)", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return core.PairingPollResult{}, fmt.Errorf("decode response: %w", err)
	}
	return result, nil
}

func (c *Client) Enroll(ctx context.Context, request core.EnrollRequest) (EnrollResult, error) {
	var result EnrollResult
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent/enroll", "", request, &result); err != nil {
		return EnrollResult{}, err
	}
	if result.Node.ID == "" || result.Credential == "" {
		return EnrollResult{}, fmt.Errorf("enrollment response is incomplete")
	}
	return result, nil
}

func (c *Client) Heartbeat(ctx context.Context, credential string, heartbeat core.Heartbeat) error {
	var result struct {
		Node core.Node `json:"node"`
	}
	return c.doJSON(ctx, http.MethodPost, "/api/v1/agent/heartbeat", credential, heartbeat, &result)
}

func (c *Client) UploadLogs(ctx context.Context, credential, agentLog, updateLog string) error {
	var result struct{}
	return c.doJSON(ctx, http.MethodPost, "/api/v1/agent/logs", credential, struct {
		AgentLog  string `json:"agent_log"`
		UpdateLog string `json:"update_log"`
	}{
		AgentLog:  agentLog,
		UpdateLog: updateLog,
	}, &result)
}

func (c *Client) ClaimCommand(ctx context.Context, credential string) (core.CommandClaim, bool, error) {
	response, err := c.sendJSON(ctx, http.MethodPost, "/api/v1/agent/commands/claim", credential, nil)
	if err != nil {
		return core.CommandClaim{}, false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent {
		return core.CommandClaim{}, false, nil
	}
	if err := decodeCommandResponse(response, nil); err != nil {
		return core.CommandClaim{}, false, err
	}
	var claim core.CommandClaim
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&claim); err != nil {
		return core.CommandClaim{}, false, fmt.Errorf("decode response: %w", err)
	}
	if claim.ExecutionID == "" || claim.TaskID == "" || claim.LeaseToken == "" || claim.LeaseExpiresAt.IsZero() ||
		!claim.LeaseExpiresAt.After(time.Now()) || core.ValidateCommand(claim.Shell, claim.Command, claim.TimeoutSeconds) != nil {
		return core.CommandClaim{}, false, fmt.Errorf("command claim is invalid")
	}
	return claim, true, nil
}

func (c *Client) StartCommand(ctx context.Context, credential, executionID, leaseToken string) error {
	response, err := c.sendJSON(
		ctx,
		http.MethodPost,
		"/api/v1/agent/commands/"+url.PathEscape(executionID)+"/start",
		credential,
		struct {
			LeaseToken string `json:"lease_token"`
		}{LeaseToken: leaseToken},
	)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return decodeCommandResponse(response, &struct{}{})
}

func (c *Client) CompleteCommand(ctx context.Context, credential string, completion core.CommandCompletion) error {
	response, err := c.sendJSON(
		ctx,
		http.MethodPost,
		"/api/v1/agent/commands/"+url.PathEscape(completion.ExecutionID)+"/complete",
		credential,
		completion,
	)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return decodeCommandResponse(response, &struct{}{})
}

func decodeCommandResponse(response *http.Response, result any) error {
	if response.StatusCode == http.StatusConflict {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return ErrCommandLeaseRejected
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return fmt.Errorf("request failed (%d)", response.StatusCode)
	}
	if result == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, path, credential string, body any, result any) error {
	response, err := c.sendJSON(ctx, method, path, credential, body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return fmt.Errorf("request failed (%d)", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (c *Client) sendJSON(ctx context.Context, method, path, credential string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.serverURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	return response, nil
}
