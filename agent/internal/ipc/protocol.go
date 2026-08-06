// Package ipc 定义有大小限制的本地 Agent 控制协议。
package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"aceitcenter.local/platform/agent/internal/controller"
)

const MaxMessageBytes = 64 << 10

var (
	ErrMessageTooLarge   = errors.New("IPC message exceeds 64 KiB")
	ErrInvalidMessage    = errors.New("invalid IPC message")
	ErrSensitiveResponse = errors.New("IPC response contains a sensitive field")
)

// Request 是本地 IPC 监听器接受的 JSON 信封。
type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response 是本地 IPC 监听器返回的 JSON 信封。
type Response struct {
	ID     string         `json:"id"`
	Result any            `json:"result,omitempty"`
	Error  *ResponseError `json:"error,omitempty"`
}

// ResponseError 包含可展示给用户的已脱敏错误。
type ResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Decode 读取一条有大小限制的 JSON 请求。高于 MaxMessageBytes 的限制会被收紧，
// 防止调用方意外放宽协议边界。
func Decode(reader io.Reader, maximum int) (Request, error) {
	if maximum <= 0 || maximum > MaxMessageBytes {
		maximum = MaxMessageBytes
	}
	contents, err := io.ReadAll(io.LimitReader(reader, int64(maximum)+1))
	if err != nil {
		return Request{}, fmt.Errorf("%w: read request", ErrInvalidMessage)
	}
	if len(contents) > maximum {
		return Request{}, ErrMessageTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decodeExactlyOne(decoder, &request); err != nil {
		return Request{}, fmt.Errorf("%w: decode request", ErrInvalidMessage)
	}
	if request.ID == "" || request.Method == "" {
		return Request{}, fmt.Errorf("%w: id and method are required", ErrInvalidMessage)
	}
	return request, nil
}

func decodeExactlyOne(decoder *json.Decoder, destination any) error {
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

// Encode 在确认结果没有携带敏感字段后写入一条有大小限制的 JSON 响应。
func Encode(writer io.Writer, response Response) error {
	contents, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode IPC response: %w", err)
	}
	if len(contents) > MaxMessageBytes {
		return ErrMessageTooLarge
	}
	if hasSensitiveField(contents) {
		return ErrSensitiveResponse
	}
	if _, err := writer.Write(contents); err != nil {
		return fmt.Errorf("write IPC response: %w", err)
	}
	return nil
}

func hasSensitiveField(contents []byte) bool {
	var value any
	if json.Unmarshal(contents, &value) != nil {
		return true
	}
	return hasSensitiveValue(value)
}

func hasSensitiveValue(value any) bool {
	switch value := value.(type) {
	case []any:
		for _, item := range value {
			if hasSensitiveValue(item) {
				return true
			}
		}
	case map[string]any:
		for key, item := range value {
			if isSensitiveField(key) || hasSensitiveValue(item) {
				return true
			}
		}
	}
	return false
}

func isSensitiveField(key string) bool {
	key = strings.ToLower(key)
	return key == "pairing_credential" || strings.Contains(key, "token") || strings.Contains(key, "credential") || key == "authorization" || key == "password"
}

// Controller 是可通过 IPC 调用的安全生命周期接口。
type Controller interface {
	Status() controller.Status
	StartPairing(context.Context, string) error
	RestartWorker(context.Context) error
	CheckUpdate(context.Context) (controller.UpdateStatus, error)
	CreateDiagnostics(context.Context) (string, error)
}

// Router 强制执行 IPC 方法 allowlist。
type Router struct {
	controller Controller
}

func NewRouter(controller Controller) *Router {
	return &Router{controller: controller}
}

// Handle 分发已验证请求，并始终返回已脱敏结果。
func (r *Router) Handle(ctx context.Context, request Request) Response {
	if r.controller == nil {
		return failure(request.ID, "internal_error", "controller is unavailable")
	}
	switch request.Method {
	case "status.get":
		return Response{ID: request.ID, Result: safeStatus(r.controller.Status())}
	case "pairing.start":
		return r.startPairing(ctx, request)
	case "worker.restart":
		if err := r.controller.RestartWorker(ctx); err != nil {
			return errorResponse(request.ID, "worker_restart_failed")
		}
		return Response{ID: request.ID, Result: safeStatus(r.controller.Status())}
	case "update.check":
		result, err := r.controller.CheckUpdate(ctx)
		if err != nil {
			return errorResponse(request.ID, "update_check_failed")
		}
		return Response{ID: request.ID, Result: safeUpdateStatus(result)}
	case "diagnostics.create":
		path, err := r.controller.CreateDiagnostics(ctx)
		if err != nil {
			return errorResponse(request.ID, "diagnostics_failed")
		}
		return Response{ID: request.ID, Result: diagnosticsResult{Path: path}}
	default:
		return failure(request.ID, "method_not_allowed", "method is not allowed")
	}
}

type pairingParams struct {
	ServerURL string `json:"server_url"`
}

type diagnosticsResult struct {
	Path string `json:"path"`
}

func (r *Router) startPairing(ctx context.Context, request Request) Response {
	var params pairingParams
	if err := decodeParams(request.Params, &params); err != nil {
		return failure(request.ID, "invalid_params", "pairing parameters are invalid")
	}
	if err := r.controller.StartPairing(ctx, params.ServerURL); err != nil {
		return errorResponse(request.ID, "pairing_failed")
	}
	return Response{ID: request.ID, Result: safeStatus(r.controller.Status())}
}

func decodeParams(contents json.RawMessage, destination any) error {
	if len(contents) == 0 {
		return errors.New("missing parameters")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	return decodeExactlyOne(decoder, destination)
}

func errorResponse(id, code string) Response {
	return failure(id, code, stableErrorMessage(code))
}

func stableErrorMessage(code string) string {
	switch code {
	case "pairing_failed":
		return "pairing failed"
	case "worker_restart_failed":
		return "worker restart failed"
	case "update_check_failed":
		return "update check failed"
	case "diagnostics_failed":
		return "diagnostics creation failed"
	default:
		return "agent operation failed"
	}
}

func failure(id, code, message string) Response {
	return Response{ID: id, Error: &ResponseError{Code: code, Message: message}}
}

func safeStatus(status controller.Status) controller.Status {
	status.ServerURL = publicURL(status.ServerURL)
	if status.Error != "" {
		status.Error = "agent operation failed"
	}
	return status
}

func safeUpdateStatus(status controller.UpdateStatus) controller.UpdateStatus {
	status.URL = publicURL(status.URL)
	return status
}

func publicURL(value string) string {
	if value == "" {
		return ""
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	projected := &url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: parsed.Path, RawPath: parsed.RawPath}
	return strings.TrimRight(projected.String(), "/")
}
