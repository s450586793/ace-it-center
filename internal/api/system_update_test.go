package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aceitcenter.local/platform/internal/systemupdate"
	"aceitcenter.local/platform/internal/updaterclient"
)

type fakeSystemUpdater struct {
	status       systemupdate.StatusView
	check        systemupdate.StatusView
	statusErr    error
	checkErr     error
	statusCalls  int
	checkCalls   int
	start        systemupdate.TaskView
	startErr     error
	startVersion string
}

func (updater *fakeSystemUpdater) Status(context.Context) (systemupdate.StatusView, error) {
	updater.statusCalls++
	return updater.status, updater.statusErr
}

func (updater *fakeSystemUpdater) Check(context.Context) (systemupdate.StatusView, error) {
	updater.checkCalls++
	return updater.check, updater.checkErr
}

func (updater *fakeSystemUpdater) Start(_ context.Context, targetVersion string) (systemupdate.TaskView, error) {
	updater.startVersion = targetVersion
	return updater.start, updater.startErr
}

func TestSystemUpdateRoutesRequireOwnerSessionBeforeUpdaterAvailability(t *testing.T) {
	t.Parallel()

	router := NewRouterWithOptions(&fakeRepository{}, RouterOptions{})
	for _, test := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/v1/system/update"},
		{method: http.MethodPost, path: "/api/v1/system/update/check"},
		{method: http.MethodPost, path: "/api/v1/system/update"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			t.Parallel()
			response := requestJSON(t, router, test.method, test.path, map[string]string{"target_version": "v0.4.1"}, nil)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestSystemUpdateRoutesProxyOwnerRequests(t *testing.T) {
	t.Parallel()

	updater := &fakeSystemUpdater{
		status: systemupdate.StatusView{Current: systemupdate.VersionPairView{Backend: "v0.4.0", Web: "v0.4.0"}},
		check:  systemupdate.StatusView{Latest: &systemupdate.ReleaseView{VersionPairView: systemupdate.VersionPairView{Backend: "v0.4.1", Web: "v0.4.1"}}},
		start:  systemupdate.TaskView{ID: "task-1", To: systemupdate.VersionPairView{Backend: "v0.4.1", Web: "v0.4.1"}},
	}
	router := NewRouterWithOptions(authenticatedRepository(), RouterOptions{Now: func() time.Time { return fixedNow }, SystemUpdater: updater})
	cookie := &http.Cookie{Name: sessionCookieName, Value: "authenticated-session"}

	status := requestSystemUpdate(router, http.MethodGet, "/api/v1/system/update", "", cookie)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"backend":"v0.4.0"`) {
		t.Fatalf("status response=%d body=%s", status.Code, status.Body.String())
	}
	check := requestSystemUpdate(router, http.MethodPost, "/api/v1/system/update/check", "", cookie)
	if check.Code != http.StatusOK || !strings.Contains(check.Body.String(), `"backend":"v0.4.1"`) {
		t.Fatalf("check response=%d body=%s", check.Code, check.Body.String())
	}
	start := requestJSON(t, router, http.MethodPost, "/api/v1/system/update", map[string]string{"target_version": "v0.4.1"}, cookie)
	if start.Code != http.StatusAccepted || updater.startVersion != "v0.4.1" {
		t.Fatalf("start response=%d target=%q body=%s", start.Code, updater.startVersion, start.Body.String())
	}
}

func TestSystemUpdateStatusAndCheckRejectNonemptyRequestBodies(t *testing.T) {
	t.Parallel()

	for _, route := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/v1/system/update"},
		{method: http.MethodPost, path: "/api/v1/system/update/check"},
	} {
		for _, body := range []string{
			`{"unknown":true}`,
			`{} {}`,
			strings.Repeat("x", 1025),
		} {
			route, body := route, body
			t.Run(route.method+" "+route.path, func(t *testing.T) {
				t.Parallel()
				updater := &fakeSystemUpdater{}
				router := NewRouterWithOptions(authenticatedRepository(), RouterOptions{Now: func() time.Time { return fixedNow }, SystemUpdater: updater})
				response := requestSystemUpdate(router, route.method, route.path, body, &http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})
				if response.Code != http.StatusBadRequest || response.Body.String() != `{"error":"invalid update request"}` {
					t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
				}
				if updater.statusCalls != 0 || updater.checkCalls != 0 {
					t.Fatalf("updater calls status=%d check=%d", updater.statusCalls, updater.checkCalls)
				}
			})
		}
	}

}

func TestSystemUpdateStartRejectsUnsafeRequestBodies(t *testing.T) {
	t.Parallel()

	updater := &fakeSystemUpdater{}
	router := NewRouterWithOptions(authenticatedRepository(), RouterOptions{Now: func() time.Time { return fixedNow }, SystemUpdater: updater})
	for _, body := range []string{
		`{"target_version":"v0.4.1","image":"ghcr.io/example/unsafe"}`,
		`{"target_version":"v0.4.1"} {}`,
		`{"target_version":"` + strings.Repeat("v", 2048) + `"}`,
	} {
		response := requestRawSystemUpdate(router, body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%q status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
	if updater.startVersion != "" {
		t.Fatalf("updater received unsafe target %q", updater.startVersion)
	}
}

func TestSystemUpdateMapsConflictAndInternalFailuresWithoutLeakingDetails(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		updaterErr error
		wantStatus int
	}{
		{name: "conflict", updaterErr: updaterclient.ErrConflict, wantStatus: http.StatusConflict},
		{name: "internal", updaterErr: errors.New("docker registry password database-secret"), wantStatus: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			updater := &fakeSystemUpdater{startErr: test.updaterErr}
			router := NewRouterWithOptions(authenticatedRepository(), RouterOptions{Now: func() time.Time { return fixedNow }, SystemUpdater: updater})
			response := requestJSON(t, router, http.MethodPost, "/api/v1/system/update", map[string]string{"target_version": "v0.4.1"}, &http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "database-secret") || strings.Contains(response.Body.String(), "docker") {
				t.Fatalf("response leaked updater error: %s", response.Body.String())
			}
		})
	}
}

func TestSystemUpdateNilUpdaterReturnsSafeServiceUnavailableToOwner(t *testing.T) {
	t.Parallel()

	router := NewRouterWithOptions(authenticatedRepository(), RouterOptions{Now: func() time.Time { return fixedNow }})
	response := requestSystemUpdate(router, http.MethodGet, "/api/v1/system/update", "", &http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != `{"error":"update service unavailable"}` {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func requestRawSystemUpdate(handler http.Handler, body string) *httptest.ResponseRecorder {
	return requestSystemUpdate(handler, http.MethodPost, "/api/v1/system/update", body, &http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})
}

func requestSystemUpdate(handler http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
