package systemupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const maxStartRequestBytes int64 = 1 << 10

// UpdateManager is the public-safe portion of Manager exposed by the updater API.
type UpdateManager interface {
	Status() StatusView
	Check(context.Context) (StatusView, error)
	Start(context.Context, string) (TaskView, error)
}

// StartRequest identifies the version accepted by the last update check.
type StartRequest struct {
	TargetVersion string `json:"target_version"`
}

type updateHTTPHandler struct {
	manager   UpdateManager
	tokenHash [sha256.Size]byte
}

// NewHTTPHandler builds the fixed internal updater API.
func NewHTTPHandler(manager UpdateManager, token string) (http.Handler, error) {
	if manager == nil {
		return nil, errors.New("update manager is required")
	}
	if token == "" {
		return nil, errors.New("updater token is required")
	}
	return &updateHTTPHandler{manager: manager, tokenHash: sha256.Sum256([]byte(token))}, nil
}

func (handler *updateHTTPHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/health" {
		if request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(writer, http.StatusOK, []byte(`{"status":"ok"}`))
		return
	}

	if !isInternalUpdateRoute(request.Method, request.URL.Path) {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	if !handler.authorized(request.Header.Get("Authorization")) {
		writeJSON(writer, http.StatusUnauthorized, []byte(`{"error":"unauthorized"}`))
		return
	}

	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/internal/v1/update":
		handler.status(writer)
	case request.Method == http.MethodPost && request.URL.Path == "/internal/v1/update/check":
		handler.check(writer, request)
	case request.Method == http.MethodPost && request.URL.Path == "/internal/v1/update":
		handler.start(writer, request)
	}
}

func isInternalUpdateRoute(method, path string) bool {
	return (method == http.MethodGet && path == "/internal/v1/update") ||
		(method == http.MethodPost && (path == "/internal/v1/update" || path == "/internal/v1/update/check"))
}

func (handler *updateHTTPHandler) authorized(authorization string) bool {
	token, hasPrefix := strings.CutPrefix(authorization, "Bearer ")
	providedHash := sha256.Sum256([]byte(token))
	matches := subtle.ConstantTimeCompare(handler.tokenHash[:], providedHash[:]) == 1
	return hasPrefix && matches
}

func (handler *updateHTTPHandler) status(writer http.ResponseWriter) {
	writeJSONValue(writer, http.StatusOK, handler.manager.Status())
}

func (handler *updateHTTPHandler) check(writer http.ResponseWriter, request *http.Request) {
	status, err := handler.manager.Check(request.Context())
	if err != nil {
		writeServiceUnavailable(writer)
		return
	}
	writeJSONValue(writer, http.StatusOK, status)
}

func (handler *updateHTTPHandler) start(writer http.ResponseWriter, request *http.Request) {
	startRequest, err := decodeStartRequest(request)
	if err != nil || ValidateVersion(startRequest.TargetVersion) != nil {
		writeJSON(writer, http.StatusBadRequest, []byte(`{"error":"invalid update request"}`))
		return
	}
	task, err := handler.manager.Start(request.Context(), startRequest.TargetVersion)
	if err != nil {
		if isStartConflict(err) {
			writeJSON(writer, http.StatusConflict, []byte(`{"error":"update cannot be started"}`))
			return
		}
		writeServiceUnavailable(writer)
		return
	}
	writeJSONValue(writer, http.StatusAccepted, task)
}

func decodeStartRequest(request *http.Request) (StartRequest, error) {
	body, err := io.ReadAll(io.LimitReader(request.Body, maxStartRequestBytes+1))
	if err != nil {
		return StartRequest{}, err
	}
	if int64(len(body)) > maxStartRequestBytes {
		return StartRequest{}, errors.New("update request exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var startRequest StartRequest
	if err := decoder.Decode(&startRequest); err != nil {
		return StartRequest{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return StartRequest{}, errors.New("multiple JSON values")
		}
		return StartRequest{}, err
	}
	return startRequest, nil
}

func isStartConflict(err error) bool {
	return err.Error() == "system update task is already active" ||
		err.Error() == "system update check has expired" ||
		err.Error() == "system update is not available" ||
		err.Error() == "system update target does not match the last check"
}

func writeServiceUnavailable(writer http.ResponseWriter) {
	writeJSON(writer, http.StatusServiceUnavailable, []byte(`{"error":"update service unavailable"}`))
}

func writeJSONValue(writer http.ResponseWriter, status int, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		writeServiceUnavailable(writer)
		return
	}
	writeJSON(writer, status, encoded)
}

func writeJSON(writer http.ResponseWriter, status int, body []byte) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(body)
}
