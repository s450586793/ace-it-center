package systemupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	if err == nil {
		t.Fatal("Resolve accepted a non-stable tag")
	}
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
