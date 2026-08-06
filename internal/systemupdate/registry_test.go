package systemupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestRegistryResolverResolvesAnonymousStableAMD64Image(t *testing.T) {
	const repositoryPath = "acme/backend"
	const version = "v0.4.1"
	const created = "2026-08-06T12:00:00Z"
	config := []byte(`{"architecture":"amd64","os":"linux","config":{"Labels":{"org.opencontainers.image.version":"` + version + `","org.opencontainers.image.created":"` + created + `"}}}`)
	configDigest := sha256Digest(config)
	imageManifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` + configDigest + `","size":` + itoa(len(config)) + `},"layers":[]}`)
	imageDigest := sha256Digest(imageManifest)
	indexManifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + imageDigest + `","size":` + itoa(len(imageManifest)) + `,"platform":{"os":"linux","architecture":"amd64"}}]}`)
	topLevelDigest := sha256Digest(indexManifest)

	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v2/":
			if request.Header.Get("Authorization") != "" {
				t.Fatalf("registry probe Authorization = %q, want empty", request.Header.Get("Authorization"))
			}
			writer.Header().Set("WWW-Authenticate", `Bearer realm="`+server.URL+`/token",service="test-registry",scope="repository:`+repositoryPath+`:pull"`)
			writer.WriteHeader(http.StatusUnauthorized)
		case "/token":
			if request.URL.Query().Get("service") != "test-registry" || request.URL.Query().Get("scope") != "repository:"+repositoryPath+":pull" {
				t.Fatalf("token query = %q", request.URL.RawQuery)
			}
			_, _ = io.WriteString(writer, `{"token":"public-token"}`)
		case "/v2/" + repositoryPath + "/manifests/stable":
			requireBearerToken(t, request)
			writeManifest(writer, indexManifest, topLevelDigest, "application/vnd.oci.image.index.v1+json")
		case "/v2/" + repositoryPath + "/manifests/" + imageDigest:
			requireBearerToken(t, request)
			writeManifest(writer, imageManifest, imageDigest, "application/vnd.oci.image.manifest.v1+json")
		case "/v2/" + repositoryPath + "/blobs/" + configDigest:
			requireBearerToken(t, request)
			writer.Header().Set("Content-Type", "application/vnd.oci.image.config.v1+json")
			writer.Header().Set("Docker-Content-Digest", configDigest)
			_, _ = writer.Write(config)
		default:
			t.Fatalf("unexpected request: %s", request.URL)
		}
	}))
	defer server.Close()

	repository := strings.TrimPrefix(server.URL, "https://") + "/" + repositoryPath
	resolver := &RegistryResolver{Transport: server.Client().Transport}
	image, err := resolver.Resolve(context.Background(), repository, "stable")
	if err != nil {
		t.Fatal(err)
	}
	if image.Version != version || image.Digest != topLevelDigest || image.Repository != repository {
		t.Fatalf("image=%#v", image)
	}
	if image.PublishedAt == nil || image.PublishedAt.Format("2006-01-02T15:04:05Z") != created {
		t.Fatalf("published at = %v", image.PublishedAt)
	}
}

func TestRegistryResolverRejectsNonStableTag(t *testing.T) {
	resolver := &RegistryResolver{}
	_, err := resolver.Resolve(context.Background(), "ghcr.io/acme/backend", "latest")
	assertSafeRegistryError(t, err, "latest")
}

func TestRegistryResolverReturnsSafeErrorsForInvalidOCILabels(t *testing.T) {
	validLabels := map[string]string{
		"org.opencontainers.image.version": "v0.4.1",
		"org.opencontainers.image.created": "2026-08-06T12:00:00Z",
	}
	for _, test := range []struct {
		name   string
		labels map[string]string
		secret string
	}{
		{
			name: "malicious version",
			labels: map[string]string{
				"org.opencontainers.image.version": "invalid-token=registry-secret\\nstack trace",
				"org.opencontainers.image.created": validLabels["org.opencontainers.image.created"],
			},
			secret: "registry-secret",
		},
		{
			name: "invalid created",
			labels: map[string]string{
				"org.opencontainers.image.version": validLabels["org.opencontainers.image.version"],
				"org.opencontainers.image.created": "created-token-not-rfc3339",
			},
			secret: "created-token",
		},
		{
			name: "missing version label",
			labels: map[string]string{
				"org.opencontainers.image.created": validLabels["org.opencontainers.image.created"],
			},
			secret: "org.opencontainers.image.version",
		},
		{
			name: "missing created label",
			labels: map[string]string{
				"org.opencontainers.image.version": validLabels["org.opencontainers.image.version"],
			},
			secret: "org.opencontainers.image.created",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolver, repository, _ := newRegistryFixture(t, test.labels, "linux", "amd64")
			_, err := resolver.Resolve(context.Background(), repository, stableTag)
			assertSafeRegistryError(t, err, test.secret)
		})
	}
}

func TestRegistryResolverRejectsIndexWithoutLinuxAMD64Image(t *testing.T) {
	labels := map[string]string{
		"org.opencontainers.image.version": "v0.4.1",
		"org.opencontainers.image.created": "2026-08-06T12:00:00Z",
	}
	resolver, repository, _ := newRegistryFixture(t, labels, "linux", "arm64")
	_, err := resolver.Resolve(context.Background(), repository, stableTag)
	assertSafeRegistryError(t, err, "arm64")
}

func assertSafeRegistryError(t *testing.T, err error, forbidden string) {
	t.Helper()
	var registryError *RegistryError
	if !errors.As(err, &registryError) {
		t.Fatalf("error = %v, want RegistryError", err)
	}
	if strings.Contains(err.Error(), forbidden) {
		t.Fatalf("unsafe error = %q, contains %q", err, forbidden)
	}
}

func newRegistryFixture(t *testing.T, labels map[string]string, os, architecture string) (*RegistryResolver, string, string) {
	t.Helper()
	const repositoryPath = "acme/backend"
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		t.Fatal(err)
	}
	config := []byte(`{"architecture":"` + architecture + `","os":"` + os + `","config":{"Labels":` + string(labelsJSON) + `}}`)
	configDigest := sha256Digest(config)
	imageManifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` + configDigest + `","size":` + itoa(len(config)) + `},"layers":[]}`)
	imageDigest := sha256Digest(imageManifest)
	indexManifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + imageDigest + `","size":` + itoa(len(imageManifest)) + `,"platform":{"os":"` + os + `","architecture":"` + architecture + `"}}]}`)
	topLevelDigest := sha256Digest(indexManifest)

	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v2/":
			writer.Header().Set("WWW-Authenticate", `Bearer realm="`+server.URL+`/token",service="test-registry",scope="repository:`+repositoryPath+`:pull"`)
			writer.WriteHeader(http.StatusUnauthorized)
		case "/token":
			_, _ = io.WriteString(writer, `{"token":"public-token"}`)
		case "/v2/" + repositoryPath + "/manifests/stable":
			writeManifest(writer, indexManifest, topLevelDigest, "application/vnd.oci.image.index.v1+json")
		case "/v2/" + repositoryPath + "/manifests/" + imageDigest:
			writeManifest(writer, imageManifest, imageDigest, "application/vnd.oci.image.manifest.v1+json")
		case "/v2/" + repositoryPath + "/blobs/" + configDigest:
			writer.Header().Set("Content-Type", "application/vnd.oci.image.config.v1+json")
			writer.Header().Set("Docker-Content-Digest", configDigest)
			_, _ = writer.Write(config)
		default:
			t.Fatalf("unexpected request: %s", request.URL)
		}
	}))
	t.Cleanup(server.Close)

	repository := strings.TrimPrefix(server.URL, "https://") + "/" + repositoryPath
	return &RegistryResolver{Transport: server.Client().Transport}, repository, topLevelDigest
}

func requireBearerToken(t *testing.T, request *http.Request) {
	t.Helper()
	if got := request.Header.Get("Authorization"); got != "Bearer public-token" {
		t.Fatalf("Authorization = %q, want public bearer token", got)
	}
}

func writeManifest(writer http.ResponseWriter, manifest []byte, digest, mediaType string) {
	writer.Header().Set("Content-Type", mediaType)
	writer.Header().Set("Docker-Content-Digest", digest)
	_, _ = writer.Write(manifest)
}

func sha256Digest(contents []byte) string {
	sum := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
