package systemupdate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCheckerReportsMatchingNewerStableRelease(t *testing.T) {
	checkedAt := time.Date(2026, time.August, 6, 15, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	checker := newTestChecker(
		ImagePair{Backend: testImage(testBackendRepository, "v0.4.0"), Web: testImage(testWebRepository, "v0.4.0")},
		ImagePair{Backend: testImage(testBackendRepository, "v0.4.1"), Web: testImage(testWebRepository, "v0.4.1")},
		func() time.Time { return checkedAt },
	)

	result, err := checker.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Available || result.Target.Backend.Version != "v0.4.1" || result.Target.Web.Version != "v0.4.1" {
		t.Fatalf("result=%#v", result)
	}
	if !result.CheckedAt.Equal(checkedAt.UTC()) || result.CheckedAt.Location() != time.UTC {
		t.Fatalf("CheckedAt = %v, want UTC %v", result.CheckedAt, checkedAt.UTC())
	}
}

func TestCheckerReportsNoUpdateForEqualStableRelease(t *testing.T) {
	checker := newTestChecker(
		ImagePair{Backend: testImage(testBackendRepository, "v0.4.1"), Web: testImage(testWebRepository, "v0.4.1")},
		ImagePair{Backend: testImage(testBackendRepository, "v0.4.1"), Web: testImage(testWebRepository, "v0.4.1")},
		time.Now,
	)

	result, err := checker.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Available {
		t.Fatalf("result=%#v, want no update", result)
	}
}

func TestCheckerRejectsInvalidImagePairs(t *testing.T) {
	for _, test := range []struct {
		name    string
		current ImagePair
		target  ImagePair
	}{
		{
			name:    "mismatched current versions",
			current: ImagePair{Backend: testImage(testBackendRepository, "v0.4.0"), Web: testImage(testWebRepository, "v0.3.9")},
			target:  ImagePair{Backend: testImage(testBackendRepository, "v0.4.1"), Web: testImage(testWebRepository, "v0.4.1")},
		},
		{
			name:    "mismatched stable versions",
			current: ImagePair{Backend: testImage(testBackendRepository, "v0.4.0"), Web: testImage(testWebRepository, "v0.4.0")},
			target:  ImagePair{Backend: testImage(testBackendRepository, "v0.4.1"), Web: testImage(testWebRepository, "v0.4.2")},
		},
		{
			name:    "invalid semantic version",
			current: ImagePair{Backend: testImage(testBackendRepository, "v0.4.0"), Web: testImage(testWebRepository, "v0.4.0")},
			target:  ImagePair{Backend: testImage(testBackendRepository, "v0.4.1+build"), Web: testImage(testWebRepository, "v0.4.1+build")},
		},
		{
			name:    "downgrade",
			current: ImagePair{Backend: testImage(testBackendRepository, "v0.4.1"), Web: testImage(testWebRepository, "v0.4.1")},
			target:  ImagePair{Backend: testImage(testBackendRepository, "v0.4.0"), Web: testImage(testWebRepository, "v0.4.0")},
		},
		{
			name:    "missing digest",
			current: ImagePair{Backend: Image{Repository: testBackendRepository, Version: "v0.4.0"}, Web: testImage(testWebRepository, "v0.4.0")},
			target:  ImagePair{Backend: testImage(testBackendRepository, "v0.4.1"), Web: testImage(testWebRepository, "v0.4.1")},
		},
		{
			name:    "unexpected repository",
			current: ImagePair{Backend: testImage("ghcr.io/acme/other", "v0.4.0"), Web: testImage(testWebRepository, "v0.4.0")},
			target:  ImagePair{Backend: testImage(testBackendRepository, "v0.4.1"), Web: testImage(testWebRepository, "v0.4.1")},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := newTestChecker(test.current, test.target, time.Now).Check(context.Background())
			var configurationError *ConfigurationError
			if !errors.As(err, &configurationError) {
				t.Fatalf("Check() error = %v, want ConfigurationError", err)
			}
		})
	}
}

func TestCheckerReturnsRetryableRegistryErrorForPartialLookupFailure(t *testing.T) {
	current := ImagePair{Backend: testImage(testBackendRepository, "v0.4.0"), Web: testImage(testWebRepository, "v0.4.0")}
	resolver := &fakeResolver{images: map[string]Image{testBackendRepository: testImage(testBackendRepository, "v0.4.1")}, err: errors.New("remote challenge: secret")}
	runtime := &fakeRuntime{images: map[ServiceName]Image{ServiceBackend: current.Backend, ServiceWeb: current.Web}}
	checker := NewChecker(resolver, runtime, testBackendRepository, testWebRepository, time.Now)

	_, err := checker.Check(context.Background())
	var registryError *RegistryError
	if !errors.As(err, &registryError) {
		t.Fatalf("Check() error = %v, want RegistryError", err)
	}
	if err.Error() == "" || containsSecret(err.Error()) {
		t.Fatalf("Check() unsafe registry error = %q", err)
	}
}

const (
	testBackendRepository = "ghcr.io/acme/backend"
	testWebRepository     = "ghcr.io/acme/web"
)

func newTestChecker(current, target ImagePair, now func() time.Time) *Checker {
	return NewChecker(
		&fakeResolver{images: map[string]Image{testBackendRepository: target.Backend, testWebRepository: target.Web}},
		&fakeRuntime{images: map[ServiceName]Image{ServiceBackend: current.Backend, ServiceWeb: current.Web}},
		testBackendRepository,
		testWebRepository,
		now,
	)
}

type fakeResolver struct {
	images map[string]Image
	err    error
}

func (resolver *fakeResolver) Resolve(_ context.Context, repository, tag string) (Image, error) {
	if tag != stableTag {
		return Image{}, errors.New("unexpected tag")
	}
	image, ok := resolver.images[repository]
	if !ok {
		return Image{}, errors.New("unexpected repository")
	}
	if resolver.err != nil && repository == testWebRepository {
		return Image{}, resolver.err
	}
	return image, nil
}

type fakeRuntime struct {
	images map[ServiceName]Image
}

func (runtime *fakeRuntime) InspectService(_ context.Context, service ServiceName) (Image, error) {
	image, ok := runtime.images[service]
	if !ok {
		return Image{}, errors.New("unexpected service")
	}
	return image, nil
}

func testImage(repository, version string) Image {
	return Image{Repository: repository, Version: version, Digest: "sha256:0123456789abcdef"}
}

func containsSecret(value string) bool {
	return strings.Contains(value, "secret")
}
