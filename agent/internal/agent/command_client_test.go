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

func TestClientClaimsCommandWithDeviceCredential(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().UTC().Add(35 * time.Minute).Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/agent/commands/claim" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer device-secret" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(writer).Encode(core.CommandClaim{
			ExecutionID: "execution-1", TaskID: "task-1", Shell: core.CommandShellPowerShell,
			Command: "hostname", TimeoutSeconds: 300, LeaseToken: "lease-secret", LeaseExpiresAt: expiresAt,
		})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	claim, found, err := client.ClaimCommand(context.Background(), "device-secret")
	if err != nil || !found {
		t.Fatalf("ClaimCommand = (%#v, %v, %v)", claim, found, err)
	}
	if claim.ExecutionID != "execution-1" || claim.LeaseToken != "lease-secret" || !claim.LeaseExpiresAt.Equal(expiresAt) {
		t.Fatalf("claim = %#v", claim)
	}
}

func TestClientTreatsNoCommandAsSuccessfulEmptyPoll(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	claim, found, err := client.ClaimCommand(context.Background(), "device-secret")
	if err != nil || found || claim.ExecutionID != "" {
		t.Fatalf("ClaimCommand no work = (%#v, %v, %v)", claim, found, err)
	}
}

func TestClientMapsCommandLeaseConflictWithoutExposingSecrets(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	err = client.StartCommand(context.Background(), "device-secret", "execution-1", "lease-secret")
	if !errors.Is(err, ErrCommandLeaseRejected) {
		t.Fatalf("StartCommand error = %v", err)
	}
	if err != nil && (containsSecret(err.Error(), "device-secret") || containsSecret(err.Error(), "lease-secret")) {
		t.Fatalf("error exposed a secret: %v", err)
	}
}

func TestClientStartsAndCompletesCommand(t *testing.T) {
	t.Parallel()

	requests := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer device-secret" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		body["path"] = request.URL.Path
		requests <- body
		_ = json.NewEncoder(writer).Encode(map[string]bool{"accepted": true})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	if err := client.StartCommand(context.Background(), "device-secret", "execution-1", "lease-secret"); err != nil {
		t.Fatalf("StartCommand: %v", err)
	}
	exitCode := 0
	if err := client.CompleteCommand(context.Background(), "device-secret", core.CommandCompletion{
		ExecutionID: "execution-1", LeaseToken: "lease-secret", Status: core.CommandSucceeded,
		ExitCode: &exitCode, Output: "office-pc", DurationMS: 25,
	}); err != nil {
		t.Fatalf("CompleteCommand: %v", err)
	}

	started := <-requests
	if started["path"] != "/api/v1/agent/commands/execution-1/start" || started["lease_token"] != "lease-secret" {
		t.Fatalf("start request = %#v", started)
	}
	completed := <-requests
	if completed["path"] != "/api/v1/agent/commands/execution-1/complete" || completed["lease_token"] != "lease-secret" || completed["status"] != "succeeded" || completed["output"] != "office-pc" {
		t.Fatalf("complete request = %#v", completed)
	}
}

func containsSecret(message, secret string) bool {
	for index := 0; index+len(secret) <= len(message); index++ {
		if message[index:index+len(secret)] == secret {
			return true
		}
	}
	return false
}
