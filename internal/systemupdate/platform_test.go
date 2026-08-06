package systemupdate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
)

const (
	testBackendRepo  = "ghcr.io/s450586793/ace-it-center-backend"
	testWebRepo      = "ghcr.io/s450586793/ace-it-center-web"
	testOldID        = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testOldDigest    = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testTargetID     = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testTargetDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

func TestPlatformRejectsUnmanagedServiceWithoutInvokingDocker(t *testing.T) {
	runner := &fakeCommandRunner{}
	platform := newTestPlatform(t, runner)

	_, err := platform.InspectService(context.Background(), ServiceName("postgres"))
	if err == nil || len(runner.calls) != 0 {
		t.Fatalf("InspectService() error = %v, calls = %#v", err, runner.calls)
	}
}

func TestPlatformRejectsNilContextBeforeExternalIO(t *testing.T) {
	taskID := "123e4567-e89b-12d3-a456-426614174000"
	old := cleanupImage(taskID)
	for _, test := range []struct {
		name string
		run  func(*CLIPlatform) error
	}{
		{name: "inspect", run: func(platform *CLIPlatform) error { _, err := platform.InspectService(nil, ServiceBackend); return err }},
		{name: "alias", run: func(platform *CLIPlatform) error {
			_, err := platform.CreateRollbackAlias(nil, ServiceBackend, old, taskID)
			return err
		}},
		{name: "backup", run: func(platform *CLIPlatform) error { _, err := platform.BackupDatabase(nil, taskID); return err }},
		{name: "pull", run: func(platform *CLIPlatform) error {
			return platform.PullTarget(nil, ServiceBackend, targetImagePair().Backend)
		}},
		{name: "deploy target", run: func(platform *CLIPlatform) error {
			return platform.DeployTarget(nil, ServiceBackend, targetImagePair(), taskID)
		}},
		{name: "deploy rollback", run: func(platform *CLIPlatform) error {
			return platform.DeployRollback(nil, ServiceBackend, rollbackImagePair(taskID), taskID)
		}},
		{name: "inspect rollback", run: func(platform *CLIPlatform) error {
			_, err := platform.InspectRollbackService(nil, ServiceBackend, cleanupImage(taskID), taskID)
			return err
		}},
		{name: "health", run: func(platform *CLIPlatform) error { return platform.WaitHealthy(nil, ServiceBackend) }},
		{name: "rollback health", run: func(platform *CLIPlatform) error {
			return platform.WaitRollbackHealthy(nil, ServiceBackend, cleanupImage(taskID), taskID)
		}},
		{name: "cleanup", run: func(platform *CLIPlatform) error { return platform.RemoveOldImage(nil, ServiceBackend, old) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeCommandRunner{}
			platform := newTestPlatform(t, runner)
			if err := test.run(platform); err == nil || len(runner.calls) != 0 {
				t.Fatalf("operation error = %v, calls = %#v", err, runner.calls)
			}
			for _, directory := range []string{platform.config.StateDir, platform.config.BackupDir} {
				if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("operation created %q: %v", directory, err)
				}
			}
		})
	}
}

func TestPlatformContainerDiscoveryUsesExactComposeLabelsAndRequiresOneResult(t *testing.T) {
	runner := &fakeCommandRunner{results: []commandResult{{output: []byte("first\nsecond\n")}}}
	platform := newTestPlatform(t, runner)

	_, err := platform.InspectService(context.Background(), ServiceBackend)
	if err == nil {
		t.Fatal("InspectService() accepted multiple containers")
	}
	want := []commandCall{{
		name: "docker",
		args: []string{
			"ps", "-q",
			"--filter", "label=com.docker.compose.project=ace-it-center",
			"--filter", "label=com.docker.compose.service=backend",
		},
	}}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestPlatformInspectsContainerAndImageMetadata(t *testing.T) {
	runner := &fakeCommandRunner{results: []commandResult{
		{output: []byte("container-id\n")},
		{output: []byte(`[{"Image":"` + testOldID + `","Config":{"Image":"` + testBackendRepo + `:stable","Labels":{"com.docker.compose.project":"ace-it-center","com.docker.compose.service":"backend"}},"State":{"Health":{"Status":"healthy"}}}]`)},
		{output: []byte(`[{"Id":"` + testOldID + `","RepoTags":["` + testBackendRepo + `:stable"],"RepoDigests":["` + testBackendRepo + `@` + testOldDigest + `"],"Config":{"Labels":{"org.opencontainers.image.version":"v0.4.0"}}}]`)},
	}}
	platform := newTestPlatform(t, runner)

	image, err := platform.InspectService(context.Background(), ServiceBackend)
	if err != nil {
		t.Fatal(err)
	}
	wantImage := Image{Repository: testBackendRepo, Version: "v0.4.0", Digest: testOldDigest, ID: testOldID}
	if image != wantImage {
		t.Fatalf("InspectService() = %#v, want %#v", image, wantImage)
	}
	wantCalls := []commandCall{
		{name: "docker", args: []string{"ps", "-q", "--filter", "label=com.docker.compose.project=ace-it-center", "--filter", "label=com.docker.compose.service=backend"}},
		{name: "docker", args: []string{"inspect", "container-id"}},
		{name: "docker", args: []string{"image", "inspect", testOldID}},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, wantCalls)
	}
}

func TestPlatformRejectsUntrustedInspectMetadata(t *testing.T) {
	validContainer := `[{"Image":"` + testOldID + `","Config":{"Image":"` + testBackendRepo + `:stable","Labels":{"com.docker.compose.project":"ace-it-center","com.docker.compose.service":"backend"}},"State":{"Health":{"Status":"healthy"}}}]`
	validImage := `[{"Id":"` + testOldID + `","RepoTags":["` + testBackendRepo + `:stable"],"RepoDigests":["` + testBackendRepo + `@` + testOldDigest + `"],"Config":{"Labels":{"org.opencontainers.image.version":"v0.4.0"}}}]`
	for _, test := range []struct {
		name      string
		container string
		image     string
	}{
		{name: "container image ID", container: strings.Replace(validContainer, testOldID, "sha256:short", 1), image: validImage},
		{name: "compose project", container: strings.Replace(validContainer, "ace-it-center", "other", 1), image: validImage},
		{name: "compose service", container: strings.Replace(validContainer, `"backend"`, `"web"`, 1), image: validImage},
		{name: "configured image repository", container: strings.Replace(validContainer, testBackendRepo, testWebRepo, 1), image: validImage},
		{name: "image ID mismatch", container: validContainer, image: strings.Replace(validImage, testOldID, testTargetID, 1)},
		{name: "invalid digest", container: validContainer, image: strings.Replace(validImage, testOldDigest, "sha256:short", 1)},
		{name: "unexpected digest repository", container: validContainer, image: strings.Replace(validImage, testBackendRepo+"@", testWebRepo+"@", 1)},
		{name: "invalid version", container: validContainer, image: strings.Replace(validImage, "v0.4.0", "latest", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeCommandRunner{results: []commandResult{
				{output: []byte("container-id\n")},
				{output: []byte(test.container)},
				{output: []byte(test.image)},
			}}
			_, err := newTestPlatform(t, runner).InspectService(context.Background(), ServiceBackend)
			if err == nil {
				t.Fatal("InspectService() accepted untrusted metadata")
			}
		})
	}
}

func TestPlatformCreatesRollbackAliasFromRecordedImageID(t *testing.T) {
	runner := &fakeCommandRunner{results: []commandResult{{}}}
	platform := newTestPlatform(t, runner)
	old := Image{Repository: testBackendRepo, Version: "v0.4.0", Digest: testOldDigest, ID: testOldID}

	got, err := platform.CreateRollbackAlias(context.Background(), ServiceBackend, old, "123E4567-E89B-12D3-A456-426614174000")
	if err != nil {
		t.Fatal(err)
	}
	wantAlias := "ace-it-center-rollback-backend:123e4567-e89b-12d3-a456-426614174000"
	if got.RollbackAlias != wantAlias || got.ID != old.ID || got.Repository != old.Repository || got.Version != old.Version || got.Digest != old.Digest {
		t.Fatalf("CreateRollbackAlias() = %#v", got)
	}
	wantCalls := []commandCall{{name: "docker", args: []string{"image", "tag", testOldID, wantAlias}}}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, wantCalls)
	}
}

func TestPlatformRejectsUnsafeRollbackAliasInputsBeforeDocker(t *testing.T) {
	valid := Image{Repository: testBackendRepo, Version: "v0.4.0", Digest: testOldDigest, ID: testOldID}
	for _, test := range []struct {
		name    string
		service ServiceName
		image   Image
		taskID  string
	}{
		{name: "service", service: ServiceName("postgres"), image: valid, taskID: "123e4567-e89b-12d3-a456-426614174000"},
		{name: "repository", service: ServiceBackend, image: Image{Repository: testWebRepo, Version: "v0.4.0", Digest: testOldDigest, ID: testOldID}, taskID: "123e4567-e89b-12d3-a456-426614174000"},
		{name: "image ID", service: ServiceBackend, image: Image{Repository: testBackendRepo, Version: "v0.4.0", Digest: testOldDigest, ID: "sha256:short"}, taskID: "123e4567-e89b-12d3-a456-426614174000"},
		{name: "task ID", service: ServiceBackend, image: valid, taskID: "../escape"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeCommandRunner{}
			_, err := newTestPlatform(t, runner).CreateRollbackAlias(context.Background(), test.service, test.image, test.taskID)
			if err == nil || len(runner.calls) != 0 {
				t.Fatalf("CreateRollbackAlias() error = %v, calls = %#v", err, runner.calls)
			}
		})
	}
}

func TestPlatformBacksUpDatabaseWithPasswordOnlyInEnvironment(t *testing.T) {
	runner := &fakeCommandRunner{results: []commandResult{{run: func(call commandCall) {
		fileIndex := indexOf(call.args, "--file")
		if fileIndex < 0 || fileIndex+1 >= len(call.args) {
			return
		}
		_ = os.WriteFile(call.args[fileIndex+1], []byte("custom dump"), 0o600)
	}}}}
	platform := newTestPlatform(t, runner)
	platform.now = func() time.Time {
		return time.Date(2026, time.August, 6, 8, 9, 10, 0, time.FixedZone("UTC+8", 8*60*60))
	}

	path, err := platform.BackupDatabase(context.Background(), "123E4567-E89B-12D3-A456-426614174000")
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(platform.config.BackupDir, "upgrade-20260806T000910Z-123e4567-e89b-12d3-a456-426614174000.dump")
	if path != wantPath {
		t.Fatalf("BackupDatabase() = %q, want %q", path, wantPath)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "custom dump" {
		t.Fatalf("backup contents = %q, error = %v", contents, err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %#v", runner.calls)
	}
	call := runner.calls[0]
	if call.name != "pg_dump" || !reflect.DeepEqual(call.env, []string{"PGPASSWORD=database-secret"}) {
		t.Fatalf("pg_dump call = %#v", call)
	}
	if strings.Contains(strings.Join(call.args, " "), "database-secret") {
		t.Fatalf("password leaked in argv: %#v", call.args)
	}
	fileIndex := indexOf(call.args, "--file")
	if fileIndex < 0 || fileIndex+1 >= len(call.args) || filepath.Dir(call.args[fileIndex+1]) != platform.config.BackupDir || call.args[fileIndex+1] == wantPath {
		t.Fatalf("temporary dump path is unsafe: %#v", call.args)
	}
	wantArgs := []string{"--format=custom", "--file", call.args[fileIndex+1], "--host", "postgres", "--port", "5432", "--username", "ace", "--dbname", "ace_it_center"}
	if !reflect.DeepEqual(call.args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", call.args, wantArgs)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v, error = %v", info.Mode(), err)
	}
}

func TestPlatformRejectsEmptyOrFailedDatabaseDumpWithoutFinalFile(t *testing.T) {
	for _, test := range []struct {
		name   string
		result commandResult
	}{
		{name: "empty", result: commandResult{}},
		{name: "failed", result: commandResult{err: errors.New("pg_dump exposed secret")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeCommandRunner{results: []commandResult{test.result}}
			platform := newTestPlatform(t, runner)
			platform.now = func() time.Time { return time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC) }

			path, err := platform.BackupDatabase(context.Background(), "123e4567-e89b-12d3-a456-426614174000")
			if err == nil || path != "" || strings.Contains(err.Error(), "secret") {
				t.Fatalf("BackupDatabase() = %q, %v", path, err)
			}
			entries, readErr := os.ReadDir(platform.config.BackupDir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("backup files remain: %#v", entries)
			}
		})
	}
}

func TestPlatformRejectsInvalidBackupTaskIDBeforeCommand(t *testing.T) {
	runner := &fakeCommandRunner{}
	path, err := newTestPlatform(t, runner).BackupDatabase(context.Background(), "../escape")
	if err == nil || path != "" || len(runner.calls) != 0 {
		t.Fatalf("BackupDatabase() = %q, %v; calls = %#v", path, err, runner.calls)
	}
}

func TestPlatformPullsImmutableTargetAndReinspectsLocalImage(t *testing.T) {
	reference := testBackendRepo + "@" + testTargetDigest
	runner := &fakeCommandRunner{results: []commandResult{
		{output: []byte("untrusted pull output")},
		{output: []byte(`[{"Id":"` + testTargetID + `","RepoTags":[],"RepoDigests":["` + reference + `"],"Config":{"Labels":{"org.opencontainers.image.version":"v0.4.1"}}}]`)},
	}}
	target := Image{Repository: testBackendRepo, Version: "v0.4.1", Digest: testTargetDigest}

	if err := newTestPlatform(t, runner).PullTarget(context.Background(), ServiceBackend, target); err != nil {
		t.Fatal(err)
	}
	want := []commandCall{
		{name: "docker", args: []string{"pull", reference}},
		{name: "docker", args: []string{"image", "inspect", reference}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestPlatformRejectsMismatchedPulledImage(t *testing.T) {
	valid := `[{"Id":"` + testTargetID + `","RepoTags":[],"RepoDigests":["` + testBackendRepo + `@` + testTargetDigest + `"],"Config":{"Labels":{"org.opencontainers.image.version":"v0.4.1"}}}]`
	for _, test := range []struct {
		name  string
		image string
	}{
		{name: "ID", image: strings.Replace(valid, testTargetID, "sha256:short", 1)},
		{name: "repository", image: strings.Replace(valid, testBackendRepo, testWebRepo, 1)},
		{name: "digest", image: strings.Replace(valid, testTargetDigest, testOldDigest, 1)},
		{name: "version", image: strings.Replace(valid, "v0.4.1", "v0.4.2", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeCommandRunner{results: []commandResult{{}, {output: []byte(test.image)}}}
			target := Image{Repository: testBackendRepo, Version: "v0.4.1", Digest: testTargetDigest}
			if err := newTestPlatform(t, runner).PullTarget(context.Background(), ServiceBackend, target); err == nil {
				t.Fatal("PullTarget() accepted mismatched image")
			}
		})
	}
}

func TestPlatformRejectsUnsafePullInputBeforeDocker(t *testing.T) {
	for _, test := range []struct {
		name    string
		service ServiceName
		target  Image
	}{
		{name: "service", service: ServiceName("postgres"), target: Image{Repository: testBackendRepo, Version: "v0.4.1", Digest: testTargetDigest}},
		{name: "repository", service: ServiceBackend, target: Image{Repository: testWebRepo, Version: "v0.4.1", Digest: testTargetDigest}},
		{name: "digest", service: ServiceBackend, target: Image{Repository: testBackendRepo, Version: "v0.4.1", Digest: "sha256:short"}},
		{name: "version", service: ServiceBackend, target: Image{Repository: testBackendRepo, Version: "latest", Digest: testTargetDigest}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeCommandRunner{}
			if err := newTestPlatform(t, runner).PullTarget(context.Background(), test.service, test.target); err == nil || len(runner.calls) != 0 {
				t.Fatalf("PullTarget() error = %v, calls = %#v", err, runner.calls)
			}
		})
	}
}

func TestPlatformDeploysTargetWithFixedStructuredOverride(t *testing.T) {
	runner := &fakeCommandRunner{results: []commandResult{{}}}
	platform := newTestPlatform(t, runner)
	pair := targetImagePair()
	taskID := "123E4567-E89B-12D3-A456-426614174000"

	if err := platform.DeployTarget(context.Background(), ServiceBackend, pair, taskID); err != nil {
		t.Fatal(err)
	}
	overridePath := filepath.Join(platform.config.StateDir, "overrides", "123e4567-e89b-12d3-a456-426614174000-target.yaml")
	assertOverrideFile(t, overridePath, map[string]string{
		"backend": testBackendRepo + "@" + testTargetDigest,
		"web":     testWebRepo + "@" + testOldDigest,
	})
	want := []commandCall{{name: "docker", args: []string{
		"compose", "--project-name", "ace-it-center", "--env-file", platform.config.ComposeEnvFile,
		"-f", platform.config.ComposeFile, "-f", overridePath,
		"up", "-d", "--no-deps", "--force-recreate", "backend",
	}}}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestPlatformDeploysRollbackWithRecordedAliasesOnly(t *testing.T) {
	runner := &fakeCommandRunner{results: []commandResult{{}}}
	platform := newTestPlatform(t, runner)
	taskID := "123e4567-e89b-12d3-a456-426614174000"
	pair := rollbackImagePair(taskID)

	if err := platform.DeployRollback(context.Background(), ServiceWeb, pair, taskID); err != nil {
		t.Fatal(err)
	}
	overridePath := filepath.Join(platform.config.StateDir, "overrides", taskID+"-rollback.yaml")
	assertOverrideFile(t, overridePath, map[string]string{
		"backend": "ace-it-center-rollback-backend:" + taskID,
		"web":     "ace-it-center-rollback-web:" + taskID,
	})
	want := []commandCall{{name: "docker", args: []string{
		"compose", "--project-name", "ace-it-center", "--env-file", platform.config.ComposeEnvFile,
		"-f", platform.config.ComposeFile, "-f", overridePath,
		"up", "-d", "--no-deps", "--force-recreate", "web",
	}}}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestPlatformRollbackAliasDeploymentHealthAndInspectionUsePersistedIdentity(t *testing.T) {
	taskID := "123e4567-e89b-12d3-a456-426614174000"
	original := cleanupImage(taskID)
	runner := &fakeCommandRunner{results: append(
		[]commandResult{{}},
		append(rollbackServiceInspectResults("healthy", original), rollbackServiceInspectResults("healthy", original)...)...,
	)}
	requestCount := 0
	config := testPlatformConfig(t)
	config.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		if request.URL.String() != "http://backend:8080/api/v1/health" {
			t.Fatalf("health URL = %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
	})}
	platform, err := NewCLIPlatform(config, runner)
	if err != nil {
		t.Fatal(err)
	}
	pair := rollbackOriginalPair(taskID)

	if err := platform.DeployRollback(context.Background(), ServiceBackend, pair, taskID); err != nil {
		t.Fatal(err)
	}
	if err := platform.WaitRollbackHealthy(context.Background(), ServiceBackend, original, taskID); err != nil {
		t.Fatal(err)
	}
	inspected, err := platform.InspectRollbackService(context.Background(), ServiceBackend, original, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if inspected != original || requestCount != 1 {
		t.Fatalf("inspected = %#v, requests = %d", inspected, requestCount)
	}
	overridePath := filepath.Join(platform.config.StateDir, "overrides", taskID+"-rollback.yaml")
	wantCalls := []commandCall{
		{name: "docker", args: []string{"compose", "--project-name", "ace-it-center", "--env-file", platform.config.ComposeEnvFile, "-f", platform.config.ComposeFile, "-f", overridePath, "up", "-d", "--no-deps", "--force-recreate", "backend"}},
		{name: "docker", args: []string{"ps", "-q", "--filter", "label=com.docker.compose.project=ace-it-center", "--filter", "label=com.docker.compose.service=backend"}},
		{name: "docker", args: []string{"inspect", "container-id"}},
		{name: "docker", args: []string{"image", "inspect", testOldID}},
		{name: "docker", args: []string{"ps", "-q", "--filter", "label=com.docker.compose.project=ace-it-center", "--filter", "label=com.docker.compose.service=backend"}},
		{name: "docker", args: []string{"inspect", "container-id"}},
		{name: "docker", args: []string{"image", "inspect", testOldID}},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, wantCalls)
	}
}

func TestPlatformRollbackInspectionRejectsAliasOrMetadataOutsidePersistedTask(t *testing.T) {
	taskID := "123e4567-e89b-12d3-a456-426614174000"
	original := cleanupImage(taskID)
	validContainer := rollbackContainerInspectJSON("healthy", original)
	validImage := rollbackImageInspectJSON(original)
	for _, test := range []struct {
		name      string
		expected  Image
		container string
		image     string
		taskID    string
	}{
		{name: "wrong task alias", expected: original, container: strings.Replace(validContainer, taskID, "223e4567-e89b-12d3-a456-426614174000", 1), image: validImage, taskID: taskID},
		{name: "wrong service alias", expected: original, container: strings.Replace(validContainer, "rollback-backend", "rollback-web", 1), image: validImage, taskID: taskID},
		{name: "running image ID", expected: original, container: strings.Replace(validContainer, testOldID, testTargetID, 1), image: validImage, taskID: taskID},
		{name: "persisted image ID", expected: Image{Repository: original.Repository, Version: original.Version, Digest: original.Digest, ID: testTargetID, RollbackAlias: original.RollbackAlias}, container: validContainer, image: validImage, taskID: taskID},
		{name: "alias absent from image", expected: original, container: validContainer, image: strings.Replace(validImage, `"`+original.RollbackAlias+`",`, "", 1), taskID: taskID},
		{name: "arbitrary local tag", expected: original, container: validContainer, image: strings.Replace(validImage, `"`+original.RollbackAlias+`"`, `"`+original.RollbackAlias+`","local/forged:tag"`, 1), taskID: taskID},
		{name: "persisted digest", expected: Image{Repository: original.Repository, Version: original.Version, Digest: testTargetDigest, ID: original.ID, RollbackAlias: original.RollbackAlias}, container: validContainer, image: validImage, taskID: taskID},
		{name: "OCI version", expected: original, container: validContainer, image: strings.Replace(validImage, "v0.4.0", "v0.4.9", 1), taskID: taskID},
		{name: "noncanonical task", expected: original, container: validContainer, image: validImage, taskID: "123E4567-E89B-12D3-A456-426614174000"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeCommandRunner{results: []commandResult{
				{output: []byte("container-id\n")},
				{output: []byte(test.container)},
				{output: []byte(test.image)},
			}}
			_, err := newTestPlatform(t, runner).InspectRollbackService(context.Background(), ServiceBackend, test.expected, test.taskID)
			if err == nil {
				t.Fatal("InspectRollbackService() accepted untrusted rollback identity")
			}
		})
	}
}

func TestPlatformNormalInspectionDoesNotTrustTaskAliasWithoutPersistedIdentity(t *testing.T) {
	taskID := "123e4567-e89b-12d3-a456-426614174000"
	original := cleanupImage(taskID)
	runner := &fakeCommandRunner{results: rollbackServiceInspectResults("healthy", original)}
	if _, err := newTestPlatform(t, runner).InspectService(context.Background(), ServiceBackend); err == nil {
		t.Fatal("InspectService() trusted a rollback alias without persisted task identity")
	}
}

func TestPlatformRejectsUnsafeDeployInputsBeforeCompose(t *testing.T) {
	taskID := "123e4567-e89b-12d3-a456-426614174000"
	for _, test := range []struct {
		name     string
		service  ServiceName
		pair     ImagePair
		taskID   string
		rollback bool
	}{
		{name: "service", service: ServiceName("postgres"), pair: targetImagePair(), taskID: taskID},
		{name: "target repository", service: ServiceBackend, pair: ImagePair{Backend: Image{Repository: testWebRepo, Version: "v0.4.1", Digest: testTargetDigest}, Web: targetImagePair().Web}, taskID: taskID},
		{name: "target digest", service: ServiceBackend, pair: ImagePair{Backend: Image{Repository: testBackendRepo, Version: "v0.4.1", Digest: "sha256:short"}, Web: targetImagePair().Web}, taskID: taskID},
		{name: "task ID", service: ServiceBackend, pair: targetImagePair(), taskID: "../escape"},
		{name: "rollback alias", service: ServiceBackend, pair: ImagePair{Backend: Image{RollbackAlias: testBackendRepo + ":latest"}, Web: rollbackImagePair(taskID).Web}, taskID: taskID, rollback: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeCommandRunner{}
			platform := newTestPlatform(t, runner)
			var err error
			if test.rollback {
				err = platform.DeployRollback(context.Background(), test.service, test.pair, test.taskID)
			} else {
				err = platform.DeployTarget(context.Background(), test.service, test.pair, test.taskID)
			}
			if err == nil || len(runner.calls) != 0 {
				t.Fatalf("deploy error = %v, calls = %#v", err, runner.calls)
			}
		})
	}
}

func TestPlatformWaitHealthyPollsDockerThenRequiresHTTP200(t *testing.T) {
	runner := &fakeCommandRunner{results: append(serviceInspectResults("starting"), serviceInspectResults("healthy")...)}
	requestCount := 0
	body := &trackingBody{reader: strings.NewReader(strings.Repeat("x", 128<<10))}
	config := testPlatformConfig(t)
	config.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		if request.Method != http.MethodGet || request.URL.String() != "http://backend:8080/api/v1/health" {
			t.Fatalf("request = %s %s", request.Method, request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
	})}
	platform, err := NewCLIPlatform(config, runner)
	if err != nil {
		t.Fatal(err)
	}
	var sleeps []time.Duration
	platform.sleep = func(_ context.Context, duration time.Duration) error {
		sleeps = append(sleeps, duration)
		return nil
	}

	if err := platform.WaitHealthy(context.Background(), ServiceBackend); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sleeps, []time.Duration{2 * time.Second}) || requestCount != 1 {
		t.Fatalf("sleeps = %v, requests = %d", sleeps, requestCount)
	}
	if !body.closed || body.read > 64<<10 {
		t.Fatalf("HTTP body closed = %v, bytes read = %d", body.closed, body.read)
	}
}

func TestPlatformWaitHealthyRejectsMissingDockerHealthWithoutHTTP(t *testing.T) {
	runner := &fakeCommandRunner{results: serviceInspectResults("")}
	requestCount := 0
	config := testPlatformConfig(t)
	config.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requestCount++
		return nil, errors.New("unexpected HTTP request")
	})}
	platform, err := NewCLIPlatform(config, runner)
	if err != nil {
		t.Fatal(err)
	}
	platform.sleep = func(context.Context, time.Duration) error { return context.DeadlineExceeded }

	err = platform.WaitHealthy(context.Background(), ServiceBackend)
	if err == nil || requestCount != 0 {
		t.Fatalf("WaitHealthy() error = %v, HTTP requests = %d", err, requestCount)
	}
}

func TestPlatformWaitHealthyRetriesNon200UntilTimeout(t *testing.T) {
	runner := &fakeCommandRunner{results: serviceInspectResults("healthy")}
	body := &trackingBody{reader: strings.NewReader("unhealthy")}
	config := testPlatformConfig(t)
	config.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: body, Header: make(http.Header)}, nil
	})}
	platform, err := NewCLIPlatform(config, runner)
	if err != nil {
		t.Fatal(err)
	}
	platform.sleep = func(context.Context, time.Duration) error { return context.DeadlineExceeded }

	err = platform.WaitHealthy(context.Background(), ServiceBackend)
	if err == nil || strings.Contains(err.Error(), "503") || !body.closed {
		t.Fatalf("WaitHealthy() error = %v, body closed = %v", err, body.closed)
	}
}

func TestPlatformWaitHealthyRejectsRedirectWithoutRequestingTarget(t *testing.T) {
	for _, statusCode := range []int{http.StatusFound, http.StatusTemporaryRedirect} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			runner := &fakeCommandRunner{results: serviceInspectResults("healthy")}
			redirectBody := &trackingBody{reader: strings.NewReader("redirect")}
			redirectTargetRequests := 0
			callerRedirectChecks := 0
			callerClient := &http.Client{
				Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					if request.URL.String() == "http://redirect.invalid/health" {
						redirectTargetRequests++
						return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
					}
					return &http.Response{
						StatusCode: statusCode,
						Body:       redirectBody,
						Header:     http.Header{"Location": []string{"http://redirect.invalid/health"}},
					}, nil
				}),
				CheckRedirect: func(*http.Request, []*http.Request) error {
					callerRedirectChecks++
					return nil
				},
			}
			config := testPlatformConfig(t)
			config.HTTPClient = callerClient
			platform, err := NewCLIPlatform(config, runner)
			if err != nil {
				t.Fatal(err)
			}
			platform.sleep = func(context.Context, time.Duration) error { return context.DeadlineExceeded }

			err = platform.WaitHealthy(context.Background(), ServiceBackend)
			if err == nil || redirectTargetRequests != 0 || callerRedirectChecks != 0 || !redirectBody.closed {
				t.Fatalf("WaitHealthy() error = %v, target requests = %d, caller redirect checks = %d, body closed = %v", err, redirectTargetRequests, callerRedirectChecks, redirectBody.closed)
			}
			if err := callerClient.CheckRedirect(nil, nil); err != nil || callerRedirectChecks != 1 {
				t.Fatalf("caller HTTP client was modified: error = %v, checks = %d", err, callerRedirectChecks)
			}
		})
	}
}

func TestPlatformWaitHealthyRejectsUnmanagedServiceWithoutIO(t *testing.T) {
	runner := &fakeCommandRunner{}
	err := newTestPlatform(t, runner).WaitHealthy(context.Background(), ServiceName("postgres"))
	if err == nil || len(runner.calls) != 0 {
		t.Fatalf("WaitHealthy() error = %v, calls = %#v", err, runner.calls)
	}
}

func TestPlatformRemoveOldImageRefusesContainerReferences(t *testing.T) {
	runner := &fakeCommandRunner{results: []commandResult{{output: []byte("stopped-container\n")}}}
	old := cleanupImage("123e4567-e89b-12d3-a456-426614174000")

	err := newTestPlatform(t, runner).RemoveOldImage(context.Background(), ServiceBackend, old)
	if err == nil || !strings.Contains(err.Error(), "cleanup pending") {
		t.Fatalf("RemoveOldImage() error = %v", err)
	}
	want := []commandCall{{name: "docker", args: []string{"ps", "-aq", "--filter", "ancestor=" + testOldID}}}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestPlatformRemoveOldImageRejectsUnknownReferencesBeforeDeletion(t *testing.T) {
	old := cleanupImage("123e4567-e89b-12d3-a456-426614174000")
	for _, test := range []struct {
		name       string
		repoTags   string
		repoDigest string
	}{
		{name: "tag", repoTags: `['ghcr.io/other/project:stable']`, repoDigest: `['` + testBackendRepo + `@` + testOldDigest + `']`},
		{name: "digest", repoTags: `['` + testBackendRepo + `:stable']`, repoDigest: `['ghcr.io/other/project@` + testOldDigest + `']`},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspectJSON := `[{"Id":"` + testOldID + `","RepoTags":` + strings.ReplaceAll(test.repoTags, `'`, `"`) + `,"RepoDigests":` + strings.ReplaceAll(test.repoDigest, `'`, `"`) + `,"Config":{"Labels":{"org.opencontainers.image.version":"v0.4.0"}}}]`
			runner := &fakeCommandRunner{results: []commandResult{{}, {output: []byte(inspectJSON)}}}

			err := newTestPlatform(t, runner).RemoveOldImage(context.Background(), ServiceBackend, old)
			if err == nil || !strings.Contains(err.Error(), "cleanup pending") {
				t.Fatalf("RemoveOldImage() error = %v", err)
			}
			if len(runner.calls) != 2 {
				t.Fatalf("unknown reference triggered deletion: %#v", runner.calls)
			}
		})
	}
}

func TestPlatformRemoveOldImageRequiresRecordedAliasOnOldImageBeforeDeletion(t *testing.T) {
	old := cleanupImage("123e4567-e89b-12d3-a456-426614174000")
	for _, test := range []struct {
		name     string
		repoTags string
	}{
		{name: "alias missing", repoTags: `[]`},
		{name: "alias reassigned to another image", repoTags: `["` + testBackendRepo + `:stable"]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspectJSON := `[{"Id":"` + testOldID + `","RepoTags":` + test.repoTags + `,"RepoDigests":["` + testBackendRepo + `@` + testOldDigest + `"],"Config":{"Labels":{"org.opencontainers.image.version":"v0.4.0"}}}]`
			runner := &fakeCommandRunner{results: []commandResult{{}, {output: []byte(inspectJSON)}}}

			err := newTestPlatform(t, runner).RemoveOldImage(context.Background(), ServiceBackend, old)
			if err == nil || !strings.Contains(err.Error(), "cleanup pending") {
				t.Fatalf("RemoveOldImage() error = %v", err)
			}
			if len(runner.calls) != 2 {
				t.Fatalf("recorded alias mismatch triggered deletion: %#v", runner.calls)
			}
			for _, call := range runner.calls {
				if reflect.DeepEqual(call.args[:min(2, len(call.args))], []string{"image", "rm"}) {
					t.Fatalf("recorded alias mismatch triggered delete call: %#v", call)
				}
			}
		})
	}
}

func TestPlatformRemoveOldImageSortsAndDeduplicatesAllowedAliases(t *testing.T) {
	taskID := "123e4567-e89b-12d3-a456-426614174000"
	old := cleanupImage(taskID)
	inspectJSON := `[{"Id":"` + testOldID + `","RepoTags":["` + testBackendRepo + `:z","` + old.RollbackAlias + `","` + testBackendRepo + `:a","` + testBackendRepo + `:a"],"RepoDigests":["` + testBackendRepo + `@` + testOldDigest + `"],"Config":{"Labels":{"org.opencontainers.image.version":"v0.4.0"}}}]`
	runner := &fakeCommandRunner{results: []commandResult{{}, {output: []byte(inspectJSON)}, {}, {}, {}, {}}}

	if err := newTestPlatform(t, runner).RemoveOldImage(context.Background(), ServiceBackend, old); err != nil {
		t.Fatal(err)
	}
	want := []commandCall{
		{name: "docker", args: []string{"ps", "-aq", "--filter", "ancestor=" + testOldID}},
		{name: "docker", args: []string{"image", "inspect", testOldID}},
		{name: "docker", args: []string{"image", "rm", "ace-it-center-rollback-backend:" + taskID}},
		{name: "docker", args: []string{"image", "rm", testBackendRepo + ":a"}},
		{name: "docker", args: []string{"image", "rm", testBackendRepo + ":z"}},
		{name: "docker", args: []string{"image", "rm", testOldID}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
	for _, call := range runner.calls {
		joined := strings.Join(call.args, " ")
		if strings.Contains(joined, "--force") || strings.Contains(joined, "image prune") {
			t.Fatalf("unsafe cleanup call: %#v", call)
		}
	}
}

func TestPlatformRemoveOldImageReturnsPendingWhenDeletionFails(t *testing.T) {
	taskID := "123e4567-e89b-12d3-a456-426614174000"
	old := cleanupImage(taskID)
	inspectJSON := `[{"Id":"` + testOldID + `","RepoTags":["` + old.RollbackAlias + `"],"RepoDigests":["` + testBackendRepo + `@` + testOldDigest + `"],"Config":{"Labels":{"org.opencontainers.image.version":"v0.4.0"}}}]`
	runner := &fakeCommandRunner{results: []commandResult{{}, {output: []byte(inspectJSON)}, {err: errors.New("daemon secret")}}}

	err := newTestPlatform(t, runner).RemoveOldImage(context.Background(), ServiceBackend, old)
	if err == nil || !strings.Contains(err.Error(), "cleanup pending") || strings.Contains(err.Error(), "secret") || len(runner.calls) != 3 {
		t.Fatalf("RemoveOldImage() error = %v, calls = %#v", err, runner.calls)
	}
}

func TestPlatformRemoveOldImageRejectsUnsafeInputBeforeDocker(t *testing.T) {
	valid := cleanupImage("123e4567-e89b-12d3-a456-426614174000")
	for _, test := range []struct {
		name    string
		service ServiceName
		image   Image
	}{
		{name: "service", service: ServiceName("postgres"), image: valid},
		{name: "repository", service: ServiceBackend, image: Image{Repository: testWebRepo, Version: valid.Version, Digest: valid.Digest, ID: valid.ID, RollbackAlias: valid.RollbackAlias}},
		{name: "ID", service: ServiceBackend, image: Image{Repository: valid.Repository, Version: valid.Version, Digest: valid.Digest, ID: "sha256:short", RollbackAlias: valid.RollbackAlias}},
		{name: "alias", service: ServiceBackend, image: Image{Repository: valid.Repository, Version: valid.Version, Digest: valid.Digest, ID: valid.ID, RollbackAlias: testBackendRepo + ":stable"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeCommandRunner{}
			err := newTestPlatform(t, runner).RemoveOldImage(context.Background(), test.service, test.image)
			if err == nil || len(runner.calls) != 0 {
				t.Fatalf("RemoveOldImage() error = %v, calls = %#v", err, runner.calls)
			}
		})
	}
}

func cleanupImage(taskID string) Image {
	return Image{
		Repository:    testBackendRepo,
		Version:       "v0.4.0",
		Digest:        testOldDigest,
		ID:            testOldID,
		RollbackAlias: "ace-it-center-rollback-backend:" + taskID,
	}
}

func serviceInspectResults(health string) []commandResult {
	healthJSON := ""
	if health != "" {
		healthJSON = `"Health":{"Status":"` + health + `"}`
	}
	return []commandResult{
		{output: []byte("container-id\n")},
		{output: []byte(`[{"Image":"` + testOldID + `","Config":{"Image":"` + testBackendRepo + `:stable","Labels":{"com.docker.compose.project":"ace-it-center","com.docker.compose.service":"backend"}},"State":{` + healthJSON + `}}]`)},
		{output: []byte(`[{"Id":"` + testOldID + `","RepoTags":["` + testBackendRepo + `:stable"],"RepoDigests":["` + testBackendRepo + `@` + testOldDigest + `"],"Config":{"Labels":{"org.opencontainers.image.version":"v0.4.0"}}}]`)},
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type trackingBody struct {
	reader io.Reader
	read   int
	closed bool
}

func (body *trackingBody) Read(buffer []byte) (int, error) {
	count, err := body.reader.Read(buffer)
	body.read += count
	return count, err
}

func (body *trackingBody) Close() error {
	body.closed = true
	return nil
}

func targetImagePair() ImagePair {
	return ImagePair{
		Backend: Image{Repository: testBackendRepo, Version: "v0.4.1", Digest: testTargetDigest},
		Web:     Image{Repository: testWebRepo, Version: "v0.4.1", Digest: testOldDigest},
	}
}

func rollbackImagePair(taskID string) ImagePair {
	return ImagePair{
		Backend: Image{RollbackAlias: "ace-it-center-rollback-backend:" + taskID},
		Web:     Image{RollbackAlias: "ace-it-center-rollback-web:" + taskID},
	}
}

func rollbackOriginalPair(taskID string) ImagePair {
	backend := cleanupImage(taskID)
	web := Image{
		Repository:    testWebRepo,
		Version:       "v0.4.0",
		Digest:        testOldDigest,
		ID:            testOldID,
		RollbackAlias: "ace-it-center-rollback-web:" + taskID,
	}
	return ImagePair{Backend: backend, Web: web}
}

func rollbackServiceInspectResults(health string, original Image) []commandResult {
	return []commandResult{
		{output: []byte("container-id\n")},
		{output: []byte(rollbackContainerInspectJSON(health, original))},
		{output: []byte(rollbackImageInspectJSON(original))},
	}
}

func rollbackContainerInspectJSON(health string, original Image) string {
	healthJSON := ""
	if health != "" {
		healthJSON = `"Health":{"Status":"` + health + `"}`
	}
	return `[{"Image":"` + original.ID + `","Config":{"Image":"` + original.RollbackAlias + `","Labels":{"com.docker.compose.project":"ace-it-center","com.docker.compose.service":"backend"}},"State":{` + healthJSON + `}}]`
}

func rollbackImageInspectJSON(original Image) string {
	return `[{"Id":"` + original.ID + `","RepoTags":["` + original.RollbackAlias + `","` + original.Repository + `:stable"],"RepoDigests":["` + original.Repository + `@` + original.Digest + `"],"Config":{"Labels":{"org.opencontainers.image.version":"` + original.Version + `"}}}]`
}

func assertOverrideFile(t *testing.T, path string, wantImages map[string]string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Services map[string]struct {
			Image      string `yaml:"image"`
			PullPolicy string `yaml:"pull_policy"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Services) != 2 {
		t.Fatalf("override services = %#v", document.Services)
	}
	for service, wantImage := range wantImages {
		got := document.Services[service]
		if got.Image != wantImage || got.PullPolicy != "never" {
			t.Fatalf("override service %s = %#v", service, got)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("override mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestPlatformConstructorRejectsMutableCommandBoundaryConfiguration(t *testing.T) {
	valid := testPlatformConfig(t)
	tests := []struct {
		name   string
		mutate func(*PlatformConfig)
	}{
		{name: "project", mutate: func(config *PlatformConfig) { config.ProjectName = "other" }},
		{name: "relative compose", mutate: func(config *PlatformConfig) { config.ComposeFile = "compose.yaml" }},
		{name: "relative env", mutate: func(config *PlatformConfig) { config.ComposeEnvFile = ".env" }},
		{name: "relative state", mutate: func(config *PlatformConfig) { config.StateDir = "state" }},
		{name: "relative backup", mutate: func(config *PlatformConfig) { config.BackupDir = "backup" }},
		{name: "backend repository", mutate: func(config *PlatformConfig) { config.BackendRepository = "ghcr.io/example/backend" }},
		{name: "web repository", mutate: func(config *PlatformConfig) { config.WebRepository = "ghcr.io/example/web" }},
		{name: "backend health URL", mutate: func(config *PlatformConfig) { config.BackendHealthURL = "http://127.0.0.1/health" }},
		{name: "web health URL", mutate: func(config *PlatformConfig) { config.WebHealthURL = "http://127.0.0.1/health" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			if _, err := NewCLIPlatform(config, &fakeCommandRunner{}); err == nil {
				t.Fatalf("NewCLIPlatform() accepted %#v", config)
			}
		})
	}
	if _, err := NewCLIPlatform(valid, nil); err == nil {
		t.Fatal("NewCLIPlatform() accepted nil runner")
	}
}

type commandCall struct {
	env  []string
	name string
	args []string
}

type commandResult struct {
	output []byte
	err    error
	run    func(commandCall)
}

type fakeCommandRunner struct {
	calls   []commandCall
	results []commandResult
}

func (runner *fakeCommandRunner) Run(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	runner.calls = append(runner.calls, commandCall{
		env:  append([]string(nil), env...),
		name: name,
		args: append([]string(nil), args...),
	})
	if len(runner.results) == 0 {
		return nil, errors.New("unexpected command")
	}
	result := runner.results[0]
	runner.results = runner.results[1:]
	if result.run != nil {
		result.run(runner.calls[len(runner.calls)-1])
	}
	return append([]byte(nil), result.output...), result.err
}

func indexOf(values []string, want string) int {
	for index, value := range values {
		if value == want {
			return index
		}
	}
	return -1
}

func newTestPlatform(t *testing.T, runner CommandRunner) *CLIPlatform {
	t.Helper()
	platform, err := NewCLIPlatform(testPlatformConfig(t), runner)
	if err != nil {
		t.Fatal(err)
	}
	return platform
}

func testPlatformConfig(t *testing.T) PlatformConfig {
	t.Helper()
	root := t.TempDir()
	return PlatformConfig{
		ProjectName:       "ace-it-center",
		ComposeFile:       filepath.Join(root, "compose.yaml"),
		ComposeEnvFile:    filepath.Join(root, ".env"),
		StateDir:          filepath.Join(root, "state"),
		BackupDir:         filepath.Join(root, "backup"),
		BackendRepository: testBackendRepo,
		WebRepository:     testWebRepo,
		BackendHealthURL:  "http://backend:8080/api/v1/health",
		WebHealthURL:      "http://web/api/v1/health",
		HealthTimeout:     time.Second,
		HTTPClient:        &http.Client{},
		PGHost:            "postgres",
		PGPort:            "5432",
		PGDatabase:        "ace_it_center",
		PGUser:            "ace",
		PGPassword:        "database-secret",
	}
}
