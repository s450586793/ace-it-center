package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"aceitcenter.local/platform/internal/systemupdate"
	"aceitcenter.local/platform/internal/updaterclient"
	"github.com/gin-gonic/gin"
)

const maxSystemUpdateRequestBytes int64 = 1 << 10

// SystemUpdater is the public-safe system update contract used by Owner routes.
type SystemUpdater interface {
	Status(context.Context) (systemupdate.StatusView, error)
	Check(context.Context) (systemupdate.StatusView, error)
	Start(context.Context, string) (systemupdate.TaskView, error)
}

func (s *server) systemUpdateStatus(c *gin.Context) {
	if hasSystemUpdateRequestBody(c.Request.Body) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid update request"})
		return
	}
	if s.systemUpdater == nil {
		writeSystemUpdateUnavailable(c)
		return
	}
	status, err := s.systemUpdater.Status(c.Request.Context())
	if err != nil {
		writeSystemUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

func (s *server) checkSystemUpdate(c *gin.Context) {
	if invalidSystemUpdateCheckBody(c.Request.Body) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid update request"})
		return
	}
	if s.systemUpdater == nil {
		writeSystemUpdateUnavailable(c)
		return
	}
	status, err := s.systemUpdater.Check(c.Request.Context())
	if err != nil {
		writeSystemUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

func invalidSystemUpdateCheckBody(body io.Reader) bool {
	if body == nil {
		return false
	}
	encoded, err := io.ReadAll(io.LimitReader(body, maxSystemUpdateRequestBytes+1))
	if err != nil || int64(len(encoded)) > maxSystemUpdateRequestBytes {
		return true
	}
	if len(encoded) == 0 {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil || len(object) != 0 {
		return true
	}
	var extra any
	return !errors.Is(decoder.Decode(&extra), io.EOF)
}

func (s *server) startSystemUpdate(c *gin.Context) {
	request, err := decodeSystemUpdateRequest(c.Request.Body)
	if err != nil || systemupdate.ValidateVersion(request.TargetVersion) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid update request"})
		return
	}
	if s.systemUpdater == nil {
		writeSystemUpdateUnavailable(c)
		return
	}
	task, err := s.systemUpdater.Start(c.Request.Context(), request.TargetVersion)
	if err != nil {
		writeSystemUpdateError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, task)
}

func hasSystemUpdateRequestBody(body io.Reader) bool {
	if body == nil {
		return false
	}
	encoded, err := io.ReadAll(io.LimitReader(body, maxSystemUpdateRequestBytes+1))
	return err != nil || len(encoded) != 0
}

func decodeSystemUpdateRequest(body io.Reader) (systemupdate.StartRequest, error) {
	encoded, err := io.ReadAll(io.LimitReader(body, maxSystemUpdateRequestBytes+1))
	if err != nil || int64(len(encoded)) > maxSystemUpdateRequestBytes {
		return systemupdate.StartRequest{}, errors.New("invalid update request")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var request systemupdate.StartRequest
	if err := decoder.Decode(&request); err != nil {
		return systemupdate.StartRequest{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return systemupdate.StartRequest{}, errors.New("invalid update request")
	}
	return request, nil
}

func writeSystemUpdateError(c *gin.Context, err error) {
	if errors.Is(err, updaterclient.ErrConflict) {
		c.JSON(http.StatusConflict, gin.H{"error": "update cannot be started"})
		return
	}
	writeSystemUpdateUnavailable(c)
}

func writeSystemUpdateUnavailable(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "update service unavailable"})
}
