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
		ErrorCode:    "upgrade_failed",
		ErrorMessage: "upgrade failed",
	}

	got := task.View()
	if got.ID != task.ID || got.From.Backend != "v0.4.0" || got.From.Web != "v0.4.0" || got.To.Backend != "v0.4.1" || got.To.Web != "v0.4.1" || got.Stage != task.Stage || !got.CreatedAt.Equal(createdAt) || got.StartedAt != &startedAt || got.FinishedAt != &finishedAt || !got.RolledBack || got.Cleanup != CleanupComplete || got.ErrorCode != task.ErrorCode || got.ErrorMessage != task.ErrorMessage {
		t.Fatalf("Task.View() = %#v", got)
	}
}
