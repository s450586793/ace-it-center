// Package updaterclient provides the backend-only client for the updater's internal API.
package updaterclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aceitcenter.local/platform/internal/systemupdate"
)

const (
	requestTimeout   = 5 * time.Second
	maxResponseBytes = 1 << 20
)

var (
	ErrBadRequest      = errors.New("updater rejected the request")
	ErrUnauthorized    = errors.New("updater authorization failed")
	ErrConflict        = errors.New("updater request conflicts with its current state")
	ErrUnavailable     = errors.New("updater service is unavailable")
	ErrInvalidResponse = errors.New("updater returned an invalid response")
)

// APIError identifies a safe, HTTP-level updater failure.
type APIError struct {
	Status int
	cause  error
}

func (err *APIError) Error() string {
	switch err.Status {
	case http.StatusBadRequest:
		return "updater rejected the request"
	case http.StatusUnauthorized:
		return "updater authorization failed"
	case http.StatusConflict:
		return "updater request conflicts with its current state"
	default:
		return "updater service is unavailable"
	}
}

func (err *APIError) Unwrap() error {
	return err.cause
}

// Client uses the configured internal updater URL and never request-derived URLs.
type Client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
}

// New validates and fixes the updater base URL for all subsequent requests.
func New(rawURL, token string, httpClient *http.Client) (*Client, error) {
	baseURL, err := url.Parse(rawURL)
	if err != nil || baseURL.Scheme != "http" || baseURL.Host == "" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, errors.New("invalid updater URL")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("updater token is required")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	clientCopy := *httpClient
	clientCopy.Timeout = requestTimeout
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{baseURL: baseURL, token: token, httpClient: &clientCopy}, nil
}

// Status returns the updater's current public status.
func (client *Client) Status(ctx context.Context) (systemupdate.StatusView, error) {
	var status systemupdate.StatusView
	if err := client.do(ctx, http.MethodGet, "/internal/v1/update", nil, &status); err != nil {
		return systemupdate.StatusView{}, err
	}
	return status, nil
}

// Check asks the updater to refresh its public release status.
func (client *Client) Check(ctx context.Context) (systemupdate.StatusView, error) {
	var status systemupdate.StatusView
	if err := client.do(ctx, http.MethodPost, "/internal/v1/update/check", nil, &status); err != nil {
		return systemupdate.StatusView{}, err
	}
	return status, nil
}

// Start asks the updater to start the exact checked target version.
func (client *Client) Start(ctx context.Context, targetVersion string) (systemupdate.TaskView, error) {
	var task systemupdate.TaskView
	if err := client.do(ctx, http.MethodPost, "/internal/v1/update", systemupdate.StartRequest{TargetVersion: targetVersion}, &task); err != nil {
		return systemupdate.TaskView{}, err
	}
	return task, nil
}

func (client *Client) do(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return ErrUnavailable
		}
		body = bytes.NewReader(encoded)
	}
	endpoint := *client.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	endpoint.RawPath = ""
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return ErrUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.httpClient.Do(request)
	if err != nil || response == nil || response.Body == nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return statusError(response.StatusCode)
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(bodyBytes) > maxResponseBytes {
		return ErrInvalidResponse
	}
	decoder := json.NewDecoder(bytes.NewReader(bodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return ErrInvalidResponse
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrInvalidResponse
	}
	return nil
}

func statusError(status int) error {
	var cause error
	switch status {
	case http.StatusBadRequest:
		cause = ErrBadRequest
	case http.StatusUnauthorized:
		cause = ErrUnauthorized
	case http.StatusConflict:
		cause = ErrConflict
	default:
		cause = ErrUnavailable
	}
	return &APIError{Status: status, cause: cause}
}
