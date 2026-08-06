package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func TestClientUploadsBoundedAgentLogsWithDeviceCredential(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/agent/logs" {
			t.Fatalf("request = %s %s, want POST /api/v1/agent/logs", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer device-secret" {
			t.Fatalf("Authorization = %q, want device bearer credential", got)
		}
		var payload struct {
			AgentLog  string `json:"agent_log"`
			UpdateLog string `json:"update_log"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode log upload: %v", err)
		}
		if payload.AgentLog != "agent log tail" || payload.UpdateLog != "update log tail" {
			t.Fatalf("log upload = %#v", payload)
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{"accepted": true})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if err := client.UploadLogs(context.Background(), "device-secret", "agent log tail", "update log tail"); err != nil {
		t.Fatalf("UploadLogs returned error: %v", err)
	}
}

func TestNewClientRejectsNonHTTPServerURL(t *testing.T) {
	t.Parallel()

	if _, err := NewClient("file:///tmp/server", http.DefaultClient); err == nil {
		t.Fatal("NewClient accepted a non-HTTP server URL")
	}
}

func TestClientStartPairingSendsPairingCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/agent/pairings" {
			t.Fatalf("request = %s %s, want POST /api/v1/agent/pairings", request.Method, request.URL.Path)
		}
		var payload core.PairingCreateRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode pairing request: %v", err)
		}
		if payload.PairingCredential != "pairing-secret" {
			t.Fatalf("pairing credential = %q", payload.PairingCredential)
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"pairing_id": "pairing-1", "state": core.PairingPending, "expires_at": time.Now().Add(time.Minute), "poll_after_seconds": 7,
		})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, pollAfter, err := client.StartPairing(context.Background(), core.PairingCreateRequest{PairingCredential: "pairing-secret"})
	if err != nil || result.ID != "pairing-1" || pollAfter != 7*time.Second {
		t.Fatalf("result=%#v pollAfter=%v err=%v", result, pollAfter, err)
	}
}

func TestClientStartPairingDefaultsInvalidPollInterval(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(response).Encode(map[string]any{
			"pairing_id": "pairing-1", "state": core.PairingPending, "expires_at": time.Now().Add(time.Minute), "poll_after_seconds": 0,
		})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, pollAfter, err := client.StartPairing(context.Background(), core.PairingCreateRequest{})
	if err != nil || pollAfter != 5*time.Second {
		t.Fatalf("pollAfter=%v err=%v, want default 5s", pollAfter, err)
	}
}

func TestClientStartPairingRejectsIncompleteResponse(t *testing.T) {
	expiresAt := time.Now().Add(time.Minute)
	for _, test := range []struct {
		name     string
		response map[string]any
	}{
		{
			name: "missing pairing ID",
			response: map[string]any{
				"state": core.PairingPending, "expires_at": expiresAt,
			},
		},
		{
			name: "missing expiry",
			response: map[string]any{
				"pairing_id": "pairing-1", "state": core.PairingPending,
			},
		},
		{
			name: "expired",
			response: map[string]any{
				"pairing_id": "pairing-1", "state": core.PairingPending, "expires_at": time.Now().Add(-time.Minute),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				_ = json.NewEncoder(response).Encode(test.response)
			}))
			t.Cleanup(server.Close)

			client, err := NewClient(server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := client.StartPairing(context.Background(), core.PairingCreateRequest{}); err == nil {
				t.Fatal("StartPairing accepted an incomplete response")
			}
		})
	}
}

func TestPollPairingUsesBearerPairingCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer pairing-secret" {
			t.Fatalf("authorization=%q", got)
		}
		_ = json.NewEncoder(response).Encode(core.PairingPollResult{ID: "pairing-1", State: core.PairingApproved, Node: &core.Node{ID: "node-1"}})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.PollPairing(context.Background(), "pairing-1", "pairing-secret")
	if err != nil || result.Node == nil || result.Node.ID != "node-1" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestPollPairingMapsGoneStatesToSentinelErrors(t *testing.T) {
	for _, test := range []struct {
		name  string
		state core.PairingState
		want  error
	}{
		{name: "rejected", state: core.PairingRejected, want: ErrPairingRejected},
		{name: "expired", state: core.PairingExpired, want: core.ErrPairingExpired},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				response.WriteHeader(http.StatusGone)
				_ = json.NewEncoder(response).Encode(core.PairingPollResult{ID: "pairing-1", State: test.state})
			}))
			t.Cleanup(server.Close)

			client, err := NewClient(server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.PollPairing(context.Background(), "pairing-1", "pairing-secret")
			if !errors.Is(err, test.want) {
				t.Fatalf("PollPairing error = %v, want %v", err, test.want)
			}
		})
	}
}
