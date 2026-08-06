// Package systemupdate defines state and public contracts for managed Web upgrades.
package systemupdate

import (
	"fmt"
	"time"

	"golang.org/x/mod/semver"
)

type ServiceName string

const (
	ServiceBackend ServiceName = "backend"
	ServiceWeb     ServiceName = "web"
)

type Stage string

const (
	StageChecking           Stage = "checking"
	StageBackingUp          Stage = "backing_up"
	StagePulling            Stage = "pulling"
	StageSwitchingBackend   Stage = "switching_backend"
	StageCheckingBackend    Stage = "checking_backend"
	StageSwitchingWeb       Stage = "switching_web"
	StageCheckingWeb        Stage = "checking_web"
	StageStabilizing        Stage = "stabilizing"
	StageCleaning           Stage = "cleaning"
	StageRollingBack        Stage = "rolling_back"
	StageSucceeded          Stage = "succeeded"
	StageFailed             Stage = "failed"
	StageManualIntervention Stage = "manual_intervention"
)

// Terminal reports whether a task in this stage needs no further automatic work.
func (stage Stage) Terminal() bool {
	return stage == StageSucceeded || stage == StageFailed || stage == StageManualIntervention
}

type CleanupStatus string

const (
	CleanupNotRun   CleanupStatus = "not_run"
	CleanupComplete CleanupStatus = "complete"
	CleanupPending  CleanupStatus = "pending"
)

type Image struct {
	Repository    string     `json:"repository"`
	Version       string     `json:"version"`
	Digest        string     `json:"digest"`
	ID            string     `json:"id"`
	RollbackAlias string     `json:"rollback_alias"`
	PublishedAt   *time.Time `json:"published_at,omitempty"`
}

type ImagePair struct {
	Backend Image `json:"backend"`
	Web     Image `json:"web"`
}

type CheckResult struct {
	Current   ImagePair `json:"current"`
	Target    ImagePair `json:"target"`
	Available bool      `json:"available"`
	CheckedAt time.Time `json:"checked_at"`
}

type Task struct {
	ID           string        `json:"id"`
	Original     ImagePair     `json:"original"`
	Target       ImagePair     `json:"target"`
	Stage        Stage         `json:"stage"`
	BackupPath   string        `json:"backup_path"`
	CreatedAt    time.Time     `json:"created_at"`
	StartedAt    *time.Time    `json:"started_at,omitempty"`
	FinishedAt   *time.Time    `json:"finished_at,omitempty"`
	RolledBack   bool          `json:"rolled_back"`
	Cleanup      CleanupStatus `json:"cleanup"`
	ErrorCode    string        `json:"error_code,omitempty"`
	ErrorMessage string        `json:"error_message,omitempty"`
}

type PersistentState struct {
	LastCheck *CheckResult `json:"last_check,omitempty"`
	Task      *Task        `json:"task,omitempty"`
}

type VersionPairView struct {
	Backend string `json:"backend"`
	Web     string `json:"web"`
}

type TaskView struct {
	ID           string          `json:"id"`
	From         VersionPairView `json:"from"`
	To           VersionPairView `json:"to"`
	Stage        Stage           `json:"stage"`
	CreatedAt    time.Time       `json:"created_at"`
	StartedAt    *time.Time      `json:"started_at,omitempty"`
	FinishedAt   *time.Time      `json:"finished_at,omitempty"`
	RolledBack   bool            `json:"rolled_back"`
	Cleanup      CleanupStatus   `json:"cleanup"`
	ErrorCode    string          `json:"error_code,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
}

type ReleaseView struct {
	VersionPairView
	PublishedAt *time.Time `json:"published_at,omitempty"`
}

type StatusView struct {
	Current         VersionPairView `json:"current"`
	Latest          *ReleaseView    `json:"latest,omitempty"`
	UpdateAvailable bool            `json:"update_available"`
	CheckedAt       *time.Time      `json:"checked_at,omitempty"`
	Task            *TaskView       `json:"task,omitempty"`
}

var publicErrorMessages = map[string]string{
	"state_invalid":         "升级状态无效",
	"check_expired":         "升级检查结果已过期",
	"backup_failed":         "升级备份失败",
	"pull_failed":           "升级镜像拉取失败",
	"backend_switch_failed": "后端服务切换失败",
	"backend_unhealthy":     "后端服务健康检查失败",
	"web_switch_failed":     "Web 服务切换失败",
	"web_unhealthy":         "Web 服务健康检查失败",
	"stability_failed":      "升级稳定性检查失败",
	"rollback_failed":       "升级回滚失败",
	"cleanup_pending":       "升级清理未完成",
	"updater_restarted":     "升级服务已重启",
}

// ValidateVersion accepts canonical semantic versions with the required v prefix.
func ValidateVersion(version string) error {
	if !semver.IsValid(version) || semver.Canonical(version) != version {
		return fmt.Errorf("version must be canonical semantic versioning: %q", version)
	}
	return nil
}

// View returns the public task representation without runtime image identifiers.
func (task Task) View() TaskView {
	view := TaskView{
		ID: task.ID,
		From: VersionPairView{
			Backend: task.Original.Backend.Version,
			Web:     task.Original.Web.Version,
		},
		To: VersionPairView{
			Backend: task.Target.Backend.Version,
			Web:     task.Target.Web.Version,
		},
		Stage:      task.Stage,
		CreatedAt:  task.CreatedAt,
		StartedAt:  task.StartedAt,
		FinishedAt: task.FinishedAt,
		RolledBack: task.RolledBack,
		Cleanup:    task.Cleanup,
	}
	if task.ErrorCode == "" {
		return view
	}
	if message, ok := publicErrorMessages[task.ErrorCode]; ok {
		view.ErrorCode = task.ErrorCode
		view.ErrorMessage = message
		return view
	}
	view.ErrorCode = "state_invalid"
	view.ErrorMessage = publicErrorMessages[view.ErrorCode]
	return view
}
