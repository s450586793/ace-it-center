package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"aceitcenter.local/platform/agent/internal/controller"
)

type fakeController struct {
	status     controller.Status
	pairErr    error
	restartErr error
	updateErr  error
	diagErr    error
	update     controller.UpdateStatus
	pairingURL string
	pairings   int
}

func (f *fakeController) Status() controller.Status {
	return f.status
}

func (f *fakeController) StartPairing(_ context.Context, serverURL string) error {
	f.pairings++
	f.pairingURL = serverURL
	return f.pairErr
}

func (f *fakeController) RestartWorker(context.Context) error {
	return f.restartErr
}

func (f *fakeController) CheckUpdate(context.Context) (controller.UpdateStatus, error) {
	if f.update.Version == "" {
		f.update = controller.UpdateStatus{Available: true, Version: "2.0.0"}
	}
	return f.update, f.updateErr
}

func (f *fakeController) CreateDiagnostics(context.Context) (string, error) {
	return "diagnostics.zip", f.diagErr
}

func TestDecodeRejectsOversizedMessage(t *testing.T) {
	_, err := Decode(io.LimitReader(strings.NewReader(strings.Repeat("x", MaxMessageBytes+1)), MaxMessageBytes+1), MaxMessageBytes+1)

	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestDecodeRejectsUnknownRequestFields(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"id":"1","method":"status.get","credential":"secret"}`), MaxMessageBytes)

	if !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("err = %v", err)
	}
}

func TestDecodeRejectsTrailingJSONValue(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"id":"1","method":"status.get"} {"id":"2","method":"status.get"}`), MaxMessageBytes)

	if !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("err = %v", err)
	}
}

func TestDecodeRejectsTrailingJSONSyntax(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"id":"1","method":"status.get"}}`), MaxMessageBytes)

	if !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("err = %v", err)
	}
}

func TestDecodeAcceptsTrailingWhitespace(t *testing.T) {
	request, err := Decode(strings.NewReader("{\"id\":\"1\",\"method\":\"status.get\"}\n\t "), MaxMessageBytes)

	if err != nil || request.Method != "status.get" {
		t.Fatalf("Decode() = %#v, %v", request, err)
	}
}

func TestDecodeAcceptsRequestAtExactMessageLimit(t *testing.T) {
	base := []byte(`{"id":"1","method":"status.get","params":{"padding":""}}`)
	contents := []byte(`{"id":"1","method":"status.get","params":{"padding":"` + strings.Repeat("x", MaxMessageBytes-len(base)) + `"}}`)
	if len(contents) != MaxMessageBytes {
		t.Fatalf("request size = %d", len(contents))
	}

	request, err := Decode(strings.NewReader(string(contents)), MaxMessageBytes)
	if err != nil || request.Method != "status.get" {
		t.Fatalf("Decode() = %#v, %v", request, err)
	}
}

func TestRouterRejectsUnknownMethod(t *testing.T) {
	response := NewRouter(&fakeController{}).Handle(context.Background(), Request{ID: "1", Method: "credential.get"})

	if response.Error == nil || response.Error.Code != "method_not_allowed" {
		t.Fatalf("response = %#v", response)
	}
}

func TestRouterStartsPairingWithoutCredentialInResponse(t *testing.T) {
	fake := &fakeController{status: controller.Status{State: "waiting_for_approval", ServerURL: "https://it.example"}}
	response := NewRouter(fake).Handle(context.Background(), Request{ID: "1", Method: "pairing.start", Params: []byte(`{"server_url":"https://it.example"}`)})

	if response.Error != nil || fake.pairings != 1 || fake.pairingURL != "https://it.example" {
		t.Fatalf("response=%#v controller=%#v", response, fake)
	}
	encoded, err := json.Marshal(response)
	if err != nil || strings.Contains(strings.ToLower(string(encoded)), "credential") {
		t.Fatalf("response exposed pairing credential: %s", encoded)
	}
}

func TestRouterRejectsPairingTokenParameterWithoutCallingController(t *testing.T) {
	fake := &fakeController{}
	response := NewRouter(fake).Handle(context.Background(), Request{ID: "1", Method: "pairing.start", Params: []byte(`{"server_url":"https://it.example","token":"secret"}`)})

	if response.Error == nil || response.Error.Code != "invalid_params" || fake.pairings != 0 {
		t.Fatalf("response=%#v pairings=%d", response, fake.pairings)
	}
}

func TestRouterRejectsEnrollmentSubmitForWindowsGUI(t *testing.T) {
	response := NewRouter(&fakeController{}).Handle(context.Background(), Request{ID: "1", Method: "enrollment.submit", Params: []byte(`{"server_url":"https://it.example","token":"secret"}`)})
	if response.Error == nil || response.Error.Code != "method_not_allowed" {
		t.Fatalf("response = %#v", response)
	}
}

func TestRouterPairingDoesNotEchoCredentialInError(t *testing.T) {
	const credential = "pairing-secret"
	fake := &fakeController{pairErr: errors.New("pairing rejected: " + credential)}
	response := NewRouter(fake).Handle(context.Background(), Request{
		ID:     "1",
		Method: "pairing.start",
		Params: []byte(`{"server_url":"https://it.example"}`),
	})

	if fake.pairingURL != "https://it.example" {
		t.Fatalf("pairing server URL = %q", fake.pairingURL)
	}
	if response.Error == nil || response.Error.Code != "pairing_failed" {
		t.Fatalf("response = %#v", response)
	}
	if strings.Contains(response.Error.Message, credential) {
		t.Fatalf("response exposed credential: %#v", response)
	}
}

func TestRouterRejectsUnknownPairingParametersWithoutCallingController(t *testing.T) {
	fake := &fakeController{}
	response := NewRouter(fake).Handle(context.Background(), Request{
		ID:     "1",
		Method: "pairing.start",
		Params: []byte(`{"server_url":"https://it.example","unexpected":"value"}`),
	})

	if response.Error == nil || response.Error.Code != "invalid_params" {
		t.Fatalf("response = %#v", response)
	}
	if fake.pairings != 0 {
		t.Fatalf("pairing calls = %d", fake.pairings)
	}
}

func TestRouterRejectsTrailingPairingParametersWithoutCallingController(t *testing.T) {
	fake := &fakeController{}
	response := NewRouter(fake).Handle(context.Background(), Request{
		ID:     "1",
		Method: "pairing.start",
		Params: []byte(`{"server_url":"https://it.example"} {"server_url":"https://second.example"}`),
	})

	if response.Error == nil || response.Error.Code != "invalid_params" {
		t.Fatalf("response = %#v", response)
	}
	if fake.pairings != 0 {
		t.Fatalf("pairing calls = %d", fake.pairings)
	}
}

func TestRouterUsesStableErrorsWithoutSecrets(t *testing.T) {
	const secret = "credential-and-token"
	tests := []struct {
		name   string
		method string
		fake   *fakeController
	}{
		{name: "pairing", method: "pairing.start", fake: &fakeController{pairErr: errors.New(secret)}},
		{name: "restart", method: "worker.restart", fake: &fakeController{restartErr: errors.New(secret)}},
		{name: "update", method: "update.check", fake: &fakeController{updateErr: errors.New(secret)}},
		{name: "diagnostics", method: "diagnostics.create", fake: &fakeController{diagErr: errors.New(secret)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := Request{ID: "1", Method: test.method}
			if test.method == "pairing.start" {
				request.Params = []byte(`{"server_url":"https://it.example"}`)
			}
			response := NewRouter(test.fake).Handle(context.Background(), request)
			if response.Error == nil || strings.Contains(response.Error.Message, secret) {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}

func TestRouterProjectsUnsafeStatusAndUpdateURLs(t *testing.T) {
	fake := &fakeController{
		status: controller.Status{State: "error", ServerURL: "https://credential@it.example?token=secret", Error: "credential-and-token"},
		update: controller.UpdateStatus{Available: true, URL: "https://credential@updates.example?token=secret"},
	}
	router := NewRouter(fake)

	statusResponse := router.Handle(context.Background(), Request{ID: "1", Method: "status.get"})
	status := statusResponse.Result.(controller.Status)
	if status.ServerURL != "" || status.Error != "agent operation failed" {
		t.Fatalf("status = %#v", status)
	}
	updateResponse := router.Handle(context.Background(), Request{ID: "2", Method: "update.check"})
	update := updateResponse.Result.(controller.UpdateStatus)
	if update.URL != "" {
		t.Fatalf("update = %#v", update)
	}
}

func TestRouterRoutesSafeMethods(t *testing.T) {
	fake := &fakeController{status: controller.Status{State: "online", NodeID: "node-1", ServerURL: "https://it.example"}}
	router := NewRouter(fake)

	for _, method := range []string{"status.get", "worker.restart", "update.check", "diagnostics.create"} {
		t.Run(method, func(t *testing.T) {
			response := router.Handle(context.Background(), Request{ID: "1", Method: method})
			if response.Error != nil || response.Result == nil {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}

func TestEncodeRejectsSensitiveResponseFields(t *testing.T) {
	err := Encode(io.Discard, Response{ID: "1", Result: map[string]string{"credential": "secret"}})

	if !errors.Is(err, ErrSensitiveResponse) {
		t.Fatalf("err = %v", err)
	}
}

func TestEncodeAcceptsResponseAtExactMessageLimit(t *testing.T) {
	base, err := json.Marshal(Response{ID: "1", Result: map[string]string{"padding": ""}})
	if err != nil {
		t.Fatal(err)
	}
	response := Response{ID: "1", Result: map[string]string{"padding": strings.Repeat("x", MaxMessageBytes-len(base))}}
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded) != MaxMessageBytes {
		t.Fatalf("response size = %d, err = %v", len(encoded), err)
	}

	if err := Encode(io.Discard, response); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
}
