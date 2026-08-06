package systemupdate

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestTaskViewNeverExposesRuntimeIdentifiers(t *testing.T) {
	task := Task{
		ID:    "task-1",
		Stage: StagePulling,
		Original: ImagePair{Backend: Image{
			ID:            "sha256:old",
			Digest:        "sha256:old-digest",
			RollbackAlias: "ace-rollback-backend:task-1",
		}},
		Target: ImagePair{Backend: Image{
			Version: "v0.4.1",
			Digest:  "sha256:new-digest",
		}},
	}

	encoded, err := json.Marshal(task.View())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sha256:old", "sha256:old-digest", "sha256:new-digest", "ace-rollback"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("public view leaked %q: %s", secret, encoded)
		}
	}
}

func TestValidateVersionRequiresCanonicalSemver(t *testing.T) {
	if err := ValidateVersion("v0.4.1"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"0.4.1", "latest", "v0.4", "v0.4.1 evil", "v0.4.1+build"} {
		if ValidateVersion(value) == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestValidateVersionAllowsCanonicalPrerelease(t *testing.T) {
	if err := ValidateVersion("v0.4.1-rc.1"); err != nil {
		t.Fatal(err)
	}
}

func TestStageTerminal(t *testing.T) {
	for _, stage := range []Stage{StageSucceeded, StageFailed, StageManualIntervention} {
		if !stage.Terminal() {
			t.Fatalf("Stage(%q).Terminal() = false, want true", stage)
		}
	}
	for _, stage := range []Stage{StageChecking, StageRollingBack, StageCleaning} {
		if stage.Terminal() {
			t.Fatalf("Stage(%q).Terminal() = true, want false", stage)
		}
	}
}

func TestTaskViewCopiesOnlyPublicFields(t *testing.T) {
	createdAt := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	startedAt := createdAt.Add(time.Minute)
	finishedAt := startedAt.Add(time.Minute)
	task := Task{
		ID:           "task-1",
		Original:     ImagePair{Backend: Image{Version: "v0.4.0"}, Web: Image{Version: "v0.4.0"}},
		Target:       ImagePair{Backend: Image{Version: "v0.4.1"}, Web: Image{Version: "v0.4.1"}},
		Stage:        StageSucceeded,
		CreatedAt:    createdAt,
		StartedAt:    &startedAt,
		FinishedAt:   &finishedAt,
		RolledBack:   true,
		Cleanup:      CleanupComplete,
		ErrorCode:    "pull_failed",
		ErrorMessage: "internal Docker pull error",
	}

	got := task.View()
	if got.ID != task.ID || got.From.Backend != "v0.4.0" || got.From.Web != "v0.4.0" || got.To.Backend != "v0.4.1" || got.To.Web != "v0.4.1" || got.Stage != task.Stage || !got.CreatedAt.Equal(createdAt) || got.StartedAt != &startedAt || got.FinishedAt != &finishedAt || !got.RolledBack || got.Cleanup != CleanupComplete || got.ErrorCode != "pull_failed" || got.ErrorMessage != "升级镜像拉取失败" {
		t.Fatalf("Task.View() = %#v", got)
	}
}

func TestTaskViewNeverExposesInternalErrorDetails(t *testing.T) {
	secret := "docker://registry.internal/secret\nstack trace: token=super-secret"
	task := Task{
		ErrorCode:    "unrecognized_internal_error",
		ErrorMessage: secret + string(bytes.Repeat([]byte("x"), 16<<10)),
	}

	encoded, err := json.Marshal(task.View())
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"registry.internal", "stack trace", "super-secret"} {
		if bytes.Contains(encoded, []byte(token)) {
			t.Fatalf("public view leaked internal error token %q: %s", token, encoded)
		}
	}
	view := task.View()
	if view.ErrorCode != "state_invalid" || view.ErrorMessage != "升级状态无效" {
		t.Fatalf("Task.View() error = (%q, %q)", view.ErrorCode, view.ErrorMessage)
	}
	if len(view.ErrorMessage) >= len(task.ErrorMessage) {
		t.Fatalf("public error message length = %d, internal length = %d", len(view.ErrorMessage), len(task.ErrorMessage))
	}
}

func TestTaskViewOmitsEmptyError(t *testing.T) {
	view := (Task{ErrorMessage: "internal failure"}).View()
	if view.ErrorCode != "" || view.ErrorMessage != "" {
		t.Fatalf("Task.View() error = (%q, %q), want empty", view.ErrorCode, view.ErrorMessage)
	}
}
