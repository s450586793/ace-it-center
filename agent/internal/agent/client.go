package agent

import (
	"bytes"
	"context"
	"encoding/json"
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

func NewClient(serverURL string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("server URL must use HTTP or HTTPS")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{serverURL: strings.TrimRight(serverURL, "/"), http: httpClient}, nil
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

func (c *Client) doJSON(ctx context.Context, method, path, credential string, body any, result any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.serverURL+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
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
