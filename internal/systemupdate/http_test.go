package systemupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testUpdaterToken = "correct-horse-battery-staple-0123456789"

func TestHTTPRejectsMissingAndWrongTokensWithTheSameResponse(t *testing.T) {
	handler, err := NewHTTPHandler(&fakeHTTPManager{}, testUpdaterToken)
	if err != nil {
		t.Fatal(err)
	}

	missing := serveHTTP(handler, http.MethodGet, "/internal/v1/update", "", nil)
	wrong := serveHTTP(handler, http.MethodGet, "/internal/v1/update", "Bearer wrong-token", nil)
	if missing.Code != http.StatusUnauthorized || wrong.Code != http.StatusUnauthorized {
		t.Fatalf("statuses = %d, %d; want 401", missing.Code, wrong.Code)
	}
	if missing.Body.String() != wrong.Body.String() {
		t.Fatalf("unauthorized bodies differ: %q != %q", missing.Body.String(), wrong.Body.String())
	}
	if strings.Contains(missing.Body.String(), testUpdaterToken) {
		t.Fatalf("unauthorized response leaked token: %q", missing.Body.String())
	}
}

func TestNewHTTPHandlerRequiresManagerAndToken(t *testing.T) {
	if _, err := NewHTTPHandler(nil, testUpdaterToken); err == nil {
		t.Fatal("NewHTTPHandler accepted a nil manager")
	}
	if _, err := NewHTTPHandler(&fakeHTTPManager{}, ""); err == nil {
		t.Fatal("NewHTTPHandler accepted an empty token")
	}
}

func TestHTTPHealthIsTheOnlyPublicEndpoint(t *testing.T) {
	handler, err := NewHTTPHandler(&fakeHTTPManager{}, testUpdaterToken)
	if err != nil {
		t.Fatal(err)
	}

	response := serveHTTP(handler, http.MethodGet, "/health", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("health status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Body.String() != `{"status":"ok"}` {
		t.Fatalf("health body = %q", response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("health content type = %q", response.Header().Get("Content-Type"))
	}

	notFound := serveHTTP(handler, http.MethodGet, "/internal/v1/update/other", "Bearer "+testUpdaterToken, nil)
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("unknown route status = %d, want 404", notFound.Code)
	}
}

func TestHTTPServesPublicSafeStatusAndCheck(t *testing.T) {
	checkedAt := time.Date(2026, time.August, 6, 8, 0, 0, 0, time.UTC)
	manager := &fakeHTTPManager{status: StatusView{
		Current:         VersionPairView{Backend: "v1.0.0", Web: "v1.0.0"},
		Latest:          &ReleaseView{VersionPairView: VersionPairView{Backend: "v1.1.0", Web: "v1.1.0"}},
		UpdateAvailable: true,
		CheckedAt:       &checkedAt,
	}, check: StatusView{Current: VersionPairView{Backend: "v1.0.0", Web: "v1.0.0"}}}
	handler, err := NewHTTPHandler(manager, testUpdaterToken)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "status", method: http.MethodGet, path: "/internal/v1/update"},
		{name: "check", method: http.MethodPost, path: "/internal/v1/update/check"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := serveHTTP(handler, test.method, test.path, "Bearer "+testUpdaterToken, nil)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			assertNoSensitiveHTTPResponse(t, response.Body.String())
		})
	}
	if manager.checkCalls != 1 {
		t.Fatalf("Check calls = %d, want 1", manager.checkCalls)
	}
}

func TestHTTPStartStrictlyValidatesRequestBody(t *testing.T) {
	handler, err := NewHTTPHandler(&fakeHTTPManager{}, testUpdaterToken)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		body string
	}{
		{name: "unknown image", body: `{"target_version":"v1.1.0","image":"ghcr.io/example/unsafe"}`},
		{name: "unknown service", body: `{"target_version":"v1.1.0","service":"backend"}`},
		{name: "unknown path", body: `{"target_version":"v1.1.0","path":"/etc/passwd"}`},
		{name: "unknown command", body: `{"target_version":"v1.1.0","command":"docker"}`},
		{name: "trailing JSON", body: `{"target_version":"v1.1.0"} {}`},
		{name: "invalid version", body: `{"target_version":"latest"}`},
		{name: "too large", body: `{"target_version":"v1.1.0","padding":"` + strings.Repeat("x", 1024) + `"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := serveHTTP(handler, http.MethodPost, "/internal/v1/update", "Bearer "+testUpdaterToken, strings.NewReader(test.body))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			assertNoSensitiveHTTPResponse(t, response.Body.String())
		})
	}
}

func TestHTTPMapsStartOutcomesWithoutLeakingDetails(t *testing.T) {
	for _, test := range []struct {
		name       string
		startErr   error
		wantStatus int
	}{
		{name: "accepted", wantStatus: http.StatusAccepted},
		{name: "active", startErr: fmt.Errorf("request conflict: %w", ErrUpdateTaskActive), wantStatus: http.StatusConflict},
		{name: "manual", startErr: fmt.Errorf("recovery conflict: %w", ErrUpdateTaskActive), wantStatus: http.StatusConflict},
		{name: "check expired", startErr: fmt.Errorf("stale check: %w", ErrUpdateCheckExpired), wantStatus: http.StatusConflict},
		{name: "unavailable", startErr: fmt.Errorf("no release: %w", ErrUpdateUnavailable), wantStatus: http.StatusConflict},
		{name: "check mismatch", startErr: fmt.Errorf("version conflict: %w", ErrUpdateTargetMismatch), wantStatus: http.StatusConflict},
		{name: "registry", startErr: &RegistryError{}, wantStatus: http.StatusServiceUnavailable},
		{name: "platform", startErr: errors.New("docker daemon image-id sha256:deadbeef raw registry secret"), wantStatus: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &fakeHTTPManager{start: TaskView{ID: "task-1", To: VersionPairView{Backend: "v1.1.0", Web: "v1.1.0"}}, startErr: test.startErr}
			handler, err := NewHTTPHandler(manager, testUpdaterToken)
			if err != nil {
				t.Fatal(err)
			}
			response := serveHTTP(handler, http.MethodPost, "/internal/v1/update", "Bearer "+testUpdaterToken, strings.NewReader(`{"target_version":"v1.1.0"}`))
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
			if test.name == "accepted" && manager.startVersion != "v1.1.0" {
				t.Fatalf("Start target version = %q, want v1.1.0", manager.startVersion)
			}
			assertNoSensitiveHTTPResponse(t, response.Body.String())
		})
	}
}

func TestHTTPMapsRetryableCheckFailureWithoutLeakingDetails(t *testing.T) {
	manager := &fakeHTTPManager{checkErr: &RegistryError{}}
	handler, err := NewHTTPHandler(manager, testUpdaterToken)
	if err != nil {
		t.Fatal(err)
	}

	response := serveHTTP(handler, http.MethodPost, "/internal/v1/update/check", "Bearer "+testUpdaterToken, nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	assertNoSensitiveHTTPResponse(t, response.Body.String())
}

func serveHTTP(handler http.Handler, method, path, authorization string, body io.Reader) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, body)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertNoSensitiveHTTPResponse(t *testing.T, body string) {
	t.Helper()
	for _, value := range []string{"sha256:", "image-id", "rollback", "/backups/", testUpdaterToken, "raw registry secret"} {
		if strings.Contains(body, value) {
			t.Fatalf("response leaked %q: %s", value, body)
		}
	}
}

type fakeHTTPManager struct {
	status       StatusView
	check        StatusView
	checkErr     error
	checkCalls   int
	start        TaskView
	startErr     error
	startVersion string
}

func (manager *fakeHTTPManager) Status() StatusView {
	return manager.status
}

func (manager *fakeHTTPManager) Check(context.Context) (StatusView, error) {
	manager.checkCalls++
	return manager.check, manager.checkErr
}

func (manager *fakeHTTPManager) Start(_ context.Context, version string) (TaskView, error) {
	manager.startVersion = version
	return manager.start, manager.startErr
}

var _ UpdateManager = (*fakeHTTPManager)(nil)
