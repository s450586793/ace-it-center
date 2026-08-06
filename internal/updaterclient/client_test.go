package updaterclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "correct-horse-battery-staple-0123456789"

func TestClientUsesConfiguredBaseURLBearerAndTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/internal/v1/update/check" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Fatalf("Authorization = %q", got)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"current":{"backend":"v0.4.0","web":"v0.4.0"},"latest":{"backend":"v0.4.1","web":"v0.4.1"},"update_available":true}`))
	}))
	defer server.Close()

	client, err := New(server.URL, testToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if client.httpClient.Timeout != 5*time.Second {
		t.Fatalf("timeout = %s, want 5s", client.httpClient.Timeout)
	}
	status, err := client.Check(context.Background())
	if err != nil || status.Latest == nil || status.Latest.Backend != "v0.4.1" {
		t.Fatalf("status=%#v err=%v", status, err)
	}
}

func TestNewRejectsUnsafeOrIncompleteBaseURLs(t *testing.T) {
	t.Parallel()

	for _, rawURL := range []string{
		"", "https://updater:8090", "http://", "http://user:pass@updater:8090", "http://updater:8090/?next=other", "http://updater:8090/#fragment",
	} {
		t.Run(rawURL, func(t *testing.T) {
			t.Parallel()
			if _, err := New(rawURL, testToken, nil); err == nil {
				t.Fatalf("New accepted %q", rawURL)
			}
		})
	}
	if _, err := New("http://updater:8090", "", nil); err == nil {
		t.Fatal("New accepted an empty token")
	}
}

func TestNewRejectsTokensContainingWhitespace(t *testing.T) {
	t.Parallel()

	for _, token := range []string{
		"correct horse-battery-staple-0123456789",
		"correct\thorse-battery-staple-0123456789",
		"correct\nhorse-battery-staple-0123456789",
		"correct\u00a0horse-battery-staple-0123456789",
	} {
		t.Run("whitespace", func(t *testing.T) {
			t.Parallel()
			if _, err := New("http://updater:8090", token, nil); err == nil {
				t.Fatalf("New accepted whitespace token")
			} else if strings.Contains(err.Error(), token) {
				t.Fatalf("New error leaked token: %q", err)
			}
		})
	}
}

func TestClientRejectsOversizedAndMalformedResponses(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"current":{"backend":"v0.4.0","web":"v0.4.0"},"update_available":false,"unexpected":true}`},
		{name: "trailing JSON", body: `{"current":{"backend":"v0.4.0","web":"v0.4.0"},"update_available":false} {}`},
		{name: "too large", body: `{"current":{"backend":"` + strings.Repeat("x", 1<<20) + `","web":"v0.4.0"},"update_available":false}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()

			client, err := New(server.URL, testToken, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Status(context.Background())
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("Status error = %v, want invalid response", err)
			}
			if strings.Contains(err.Error(), test.body) {
				t.Fatalf("error leaked response body: %q", err)
			}
		})
	}
}

func TestClientMapsHTTPFailuresToSafeTypedErrors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		status int
		want   error
	}{
		{status: http.StatusBadRequest, want: ErrBadRequest},
		{status: http.StatusUnauthorized, want: ErrUnauthorized},
		{status: http.StatusConflict, want: ErrConflict},
		{status: http.StatusServiceUnavailable, want: ErrUnavailable},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte("docker daemon error token=" + testToken))
			}))
			defer server.Close()

			client, err := New(server.URL, testToken, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Start(context.Background(), "v0.4.1")
			if !errors.Is(err, test.want) {
				t.Fatalf("Start error = %v, want errors.Is(..., %v)", err, test.want)
			}
			var apiError *APIError
			if !errors.As(err, &apiError) || apiError.Status != test.status {
				t.Fatalf("Start error = %T %#v, want APIError status %d", err, err, test.status)
			}
			if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "docker daemon") || strings.Contains(err.Error(), server.URL) {
				t.Fatalf("error leaked upstream detail: %q", err)
			}
		})
	}
}
