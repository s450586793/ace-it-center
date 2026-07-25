package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"aceitcenter.local/platform/internal/core"
)

func TestClientEnrollSendsInventoryAndReturnsDeviceCredential(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/agent/enroll" || request.Method != http.MethodPost {
			t.Fatalf("request = %s %s, want POST /api/v1/agent/enroll", request.Method, request.URL.Path)
		}
		var payload core.EnrollRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode enrollment: %v", err)
		}
		if payload.Token != "enrollment-token" || payload.Hostname != "office-pc" || payload.MachineID != "machine-1" {
			t.Fatalf("enrollment payload = %#v", payload)
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"node":       core.Node{ID: "node-1", Name: "office-pc", Type: "linux"},
			"credential": "device-secret",
		})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	result, err := client.Enroll(context.Background(), core.EnrollRequest{
		Token: "enrollment-token", Hostname: "office-pc", Type: "linux", Version: "0.1.0", MachineID: "machine-1",
	})
	if err != nil {
		t.Fatalf("Enroll returned error: %v", err)
	}
	if result.Node.ID != "node-1" || result.Credential != "device-secret" {
		t.Fatalf("Enroll result = %#v", result)
	}
}

func TestClientHeartbeatUsesDeviceBearerCredential(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer device-secret" {
			t.Fatalf("Authorization = %q, want device bearer credential", got)
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{"node": core.Node{ID: "node-1"}})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if err := client.Heartbeat(context.Background(), "device-secret", core.Heartbeat{Hostname: "office-pc"}); err != nil {
		t.Fatalf("Heartbeat returned error: %v", err)
	}
}

func TestNewClientRejectsNonHTTPServerURL(t *testing.T) {
	t.Parallel()

	if _, err := NewClient("file:///tmp/server", http.DefaultClient); err == nil {
		t.Fatal("NewClient accepted a non-HTTP server URL")
	}
}
