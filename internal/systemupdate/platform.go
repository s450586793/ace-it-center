package systemupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/google/uuid"
)

const (
	fixedProjectName       = "ace-it-center"
	fixedBackendRepository = "ghcr.io/s450586793/ace-it-center-backend"
	fixedWebRepository     = "ghcr.io/s450586793/ace-it-center-web"
	fixedBackendHealthURL  = "http://backend:8080/api/v1/health"
	fixedWebHealthURL      = "http://web/api/v1/health"
)

var sha256Pattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type PlatformConfig struct {
	ProjectName       string
	ComposeFile       string
	ComposeEnvFile    string
	StateDir          string
	BackupDir         string
	BackendRepository string
	WebRepository     string
	BackendHealthURL  string
	WebHealthURL      string
	HealthTimeout     time.Duration
	HTTPClient        *http.Client
	PGHost            string
	PGPort            string
	PGDatabase        string
	PGUser            string
	PGPassword        string
}

type Platform interface {
	RunningImageReader
	InspectRollbackService(context.Context, ServiceName, Image, string) (Image, error)
	CreateRollbackAlias(context.Context, ServiceName, Image, string) (Image, error)
	BackupDatabase(context.Context, string) (string, error)
	PullTarget(context.Context, ServiceName, Image) error
	DeployTarget(context.Context, ServiceName, ImagePair, string) error
	DeployRollback(context.Context, ServiceName, ImagePair, string) error
	WaitHealthy(context.Context, ServiceName) error
	WaitRollbackHealthy(context.Context, ServiceName, Image, string) error
	RemoveOldImage(context.Context, ServiceName, Image) error
}

type CLIPlatform struct {
	config         PlatformConfig
	runner         CommandRunner
	healthClient   *http.Client
	sleep          func(context.Context, time.Duration) error
	healthInterval time.Duration
	now            func() time.Time
}

var _ Platform = (*CLIPlatform)(nil)

func NewCLIPlatform(config PlatformConfig, runner CommandRunner) (*CLIPlatform, error) {
	if runner == nil {
		return nil, errors.New("platform command runner is required")
	}
	if config.ProjectName != fixedProjectName ||
		config.BackendRepository != fixedBackendRepository ||
		config.WebRepository != fixedWebRepository ||
		config.BackendHealthURL != fixedBackendHealthURL ||
		config.WebHealthURL != fixedWebHealthURL {
		return nil, errors.New("platform configuration is not allowlisted")
	}
	for _, path := range []string{config.ComposeFile, config.ComposeEnvFile, config.StateDir, config.BackupDir} {
		if !filepath.IsAbs(path) {
			return nil, errors.New("platform paths must be absolute")
		}
	}
	if config.HealthTimeout <= 0 || config.HTTPClient == nil {
		return nil, errors.New("platform health configuration is required")
	}
	healthClient := *config.HTTPClient
	healthClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &CLIPlatform{
		config:         config,
		runner:         runner,
		healthClient:   &healthClient,
		sleep:          sleepContext,
		healthInterval: 2 * time.Second,
		now:            time.Now,
	}, nil
}

func (platform *CLIPlatform) InspectService(ctx context.Context, service ServiceName) (Image, error) {
	if err := requireContext(ctx); err != nil {
		return Image{}, err
	}
	metadata, err := platform.inspectService(ctx, service)
	if err != nil {
		return Image{}, err
	}
	return metadata.image, nil
}

func (platform *CLIPlatform) InspectRollbackService(ctx context.Context, service ServiceName, expected Image, taskID string) (Image, error) {
	if err := requireContext(ctx); err != nil {
		return Image{}, err
	}
	metadata, err := platform.inspectRollbackService(ctx, service, expected, taskID)
	if err != nil {
		return Image{}, err
	}
	return metadata.image, nil
}

func (platform *CLIPlatform) CreateRollbackAlias(ctx context.Context, service ServiceName, old Image, taskID string) (Image, error) {
	if err := requireContext(ctx); err != nil {
		return Image{}, err
	}
	repository, err := platform.repositoryFor(service)
	if err != nil {
		return Image{}, err
	}
	parsedTaskID, err := uuid.Parse(taskID)
	if err != nil || old.Repository != repository || !sha256Pattern.MatchString(old.ID) || !sha256Pattern.MatchString(old.Digest) || ValidateVersion(old.Version) != nil {
		return Image{}, errors.New("rollback image metadata is invalid")
	}
	alias := fmt.Sprintf("%s-rollback-%s:%s", fixedProjectName, service, parsedTaskID.String())
	if _, err := platform.runner.Run(ctx, nil, "docker", "image", "tag", old.ID, alias); err != nil {
		return Image{}, errors.New("create rollback image alias")
	}
	old.RollbackAlias = alias
	return old, nil
}

func (platform *CLIPlatform) BackupDatabase(ctx context.Context, taskID string) (string, error) {
	if err := requireContext(ctx); err != nil {
		return "", err
	}
	if platform == nil || platform.runner == nil || platform.now == nil {
		return "", errors.New("database backup platform is not configured")
	}
	parsedTaskID, err := uuid.Parse(taskID)
	if err != nil {
		return "", errors.New("database backup task ID is invalid")
	}
	if err := os.MkdirAll(platform.config.BackupDir, 0o700); err != nil {
		return "", errors.New("create database backup directory")
	}
	if err := os.Chmod(platform.config.BackupDir, 0o700); err != nil {
		return "", errors.New("secure database backup directory")
	}
	temporary, err := os.CreateTemp(platform.config.BackupDir, ".upgrade-*.dump.tmp")
	if err != nil {
		return "", errors.New("create temporary database backup")
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return "", errors.New("secure temporary database backup")
	}
	if err := temporary.Close(); err != nil {
		return "", errors.New("close temporary database backup")
	}

	_, err = platform.runner.Run(ctx, []string{"PGPASSWORD=" + platform.config.PGPassword}, "pg_dump",
		"--format=custom",
		"--file", temporaryPath,
		"--host", platform.config.PGHost,
		"--port", platform.config.PGPort,
		"--username", platform.config.PGUser,
		"--dbname", platform.config.PGDatabase,
	)
	if err != nil {
		return "", errors.New("database backup command failed")
	}
	info, err := os.Stat(temporaryPath)
	if err != nil || info.Size() == 0 {
		return "", errors.New("database backup is empty")
	}
	finalPath := filepath.Join(
		platform.config.BackupDir,
		fmt.Sprintf("upgrade-%s-%s.dump", platform.now().UTC().Format("20060102T150405Z"), parsedTaskID.String()),
	)
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return "", errors.New("finalize database backup")
	}
	return finalPath, nil
}

func (platform *CLIPlatform) PullTarget(ctx context.Context, service ServiceName, target Image) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	repository, err := platform.repositoryFor(service)
	if err != nil {
		return err
	}
	if err := validateTargetImage(target, repository); err != nil {
		return err
	}
	reference := repository + "@" + target.Digest
	if _, err := platform.runner.Run(ctx, nil, "docker", "pull", reference); err != nil {
		return errors.New("pull target image")
	}
	output, err := platform.runner.Run(ctx, nil, "docker", "image", "inspect", reference)
	if err != nil {
		return errors.New("inspect pulled target image")
	}
	inspected, err := parseImageInspect(output, repository, "")
	if err != nil || inspected.Version != target.Version || inspected.Digest != target.Digest || target.ID != "" && inspected.ID != target.ID {
		return errors.New("pulled target image metadata does not match")
	}
	return nil
}

func (platform *CLIPlatform) DeployTarget(ctx context.Context, service ServiceName, pair ImagePair, taskID string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if _, err := platform.repositoryFor(service); err != nil {
		return err
	}
	parsedTaskID, err := uuid.Parse(taskID)
	if err != nil {
		return errors.New("target deployment task ID is invalid")
	}
	if err := validateTargetPair(pair, platform.config.BackendRepository, platform.config.WebRepository); err != nil {
		return err
	}
	override := composeOverride{
		Services: composeServices{
			Backend: composeService{Image: pair.Backend.Repository + "@" + pair.Backend.Digest, PullPolicy: "never"},
			Web:     composeService{Image: pair.Web.Repository + "@" + pair.Web.Digest, PullPolicy: "never"},
		},
	}
	path, err := platform.writeOverride(parsedTaskID.String()+"-target.yaml", override)
	if err != nil {
		return err
	}
	return platform.composeUp(ctx, service, path)
}

func (platform *CLIPlatform) DeployRollback(ctx context.Context, service ServiceName, pair ImagePair, taskID string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if _, err := platform.repositoryFor(service); err != nil {
		return err
	}
	parsedTaskID, err := uuid.Parse(taskID)
	if err != nil || parsedTaskID.String() != taskID {
		return errors.New("rollback deployment task ID is invalid")
	}
	normalizedTaskID := parsedTaskID.String()
	wantBackendAlias := fixedProjectName + "-rollback-backend:" + normalizedTaskID
	wantWebAlias := fixedProjectName + "-rollback-web:" + normalizedTaskID
	if pair.Backend.RollbackAlias != wantBackendAlias || pair.Web.RollbackAlias != wantWebAlias {
		return errors.New("rollback image aliases are invalid")
	}
	override := composeOverride{
		Services: composeServices{
			Backend: composeService{Image: wantBackendAlias, PullPolicy: "never"},
			Web:     composeService{Image: wantWebAlias, PullPolicy: "never"},
		},
	}
	path, err := platform.writeOverride(normalizedTaskID+"-rollback.yaml", override)
	if err != nil {
		return err
	}
	return platform.composeUp(ctx, service, path)
}

func (platform *CLIPlatform) WaitHealthy(ctx context.Context, service ServiceName) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if _, err := platform.repositoryFor(service); err != nil {
		return err
	}
	return platform.waitHealthy(ctx, service, func(checkContext context.Context) (serviceMetadata, error) {
		return platform.inspectService(checkContext, service)
	})
}

func (platform *CLIPlatform) WaitRollbackHealthy(ctx context.Context, service ServiceName, expected Image, taskID string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if err := platform.validateRollbackIdentity(service, expected, taskID); err != nil {
		return err
	}
	return platform.waitHealthy(ctx, service, func(checkContext context.Context) (serviceMetadata, error) {
		return platform.inspectRollbackService(checkContext, service, expected, taskID)
	})
}

func (platform *CLIPlatform) waitHealthy(ctx context.Context, service ServiceName, inspect func(context.Context) (serviceMetadata, error)) error {
	healthURL := platform.config.BackendHealthURL
	if service == ServiceWeb {
		healthURL = platform.config.WebHealthURL
	}
	checkContext, cancel := context.WithTimeout(ctx, platform.config.HealthTimeout)
	defer cancel()
	for {
		metadata, err := inspect(checkContext)
		if err == nil && metadata.healthStatus == "healthy" && platform.httpHealthy(checkContext, healthURL) {
			return nil
		}
		if err := platform.sleep(checkContext, platform.healthInterval); err != nil {
			return errors.New("service health check failed")
		}
	}
}

func (platform *CLIPlatform) RemoveOldImage(ctx context.Context, service ServiceName, old Image) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	repository, err := platform.repositoryFor(service)
	if err != nil {
		return err
	}
	if err := validateCleanupImage(old, service, repository); err != nil {
		return err
	}
	output, err := platform.runner.Run(ctx, nil, "docker", "ps", "-aq", "--filter", "ancestor="+old.ID)
	if err != nil || len(strings.Fields(string(output))) != 0 {
		return cleanupPendingError()
	}
	output, err = platform.runner.Run(ctx, nil, "docker", "image", "inspect", old.ID)
	if err != nil {
		return cleanupPendingError()
	}
	var images []imageInspect
	if json.Unmarshal(output, &images) != nil || len(images) != 1 {
		return cleanupPendingError()
	}
	inspected := images[0]
	if inspected.ID != old.ID || !sha256Pattern.MatchString(inspected.ID) || inspected.Config.Labels["org.opencontainers.image.version"] != old.Version {
		return cleanupPendingError()
	}

	aliases := make(map[string]struct{}, len(inspected.RepoTags))
	foundRollbackAlias := false
	for _, tag := range inspected.RepoTags {
		if tag != old.RollbackAlias && !referenceUsesRepository(tag, repository) {
			return cleanupPendingError()
		}
		if tag == old.RollbackAlias {
			foundRollbackAlias = true
		}
		aliases[tag] = struct{}{}
	}
	if !foundRollbackAlias {
		return cleanupPendingError()
	}
	wantDigestReference := repository + "@" + old.Digest
	foundRecordedDigest := false
	for _, digestReference := range inspected.RepoDigests {
		prefix := repository + "@"
		if !strings.HasPrefix(digestReference, prefix) || !sha256Pattern.MatchString(strings.TrimPrefix(digestReference, prefix)) {
			return cleanupPendingError()
		}
		if digestReference == wantDigestReference {
			foundRecordedDigest = true
		}
	}
	if !foundRecordedDigest {
		return cleanupPendingError()
	}

	orderedAliases := make([]string, 0, len(aliases))
	for alias := range aliases {
		orderedAliases = append(orderedAliases, alias)
	}
	sort.Strings(orderedAliases)
	for _, alias := range orderedAliases {
		if _, err := platform.runner.Run(ctx, nil, "docker", "image", "rm", alias); err != nil {
			return cleanupPendingError()
		}
	}
	if _, err := platform.runner.Run(ctx, nil, "docker", "image", "rm", old.ID); err != nil {
		return cleanupPendingError()
	}
	return nil
}

func validateCleanupImage(image Image, service ServiceName, repository string) error {
	if image.Repository != repository || !sha256Pattern.MatchString(image.ID) || !sha256Pattern.MatchString(image.Digest) || ValidateVersion(image.Version) != nil {
		return errors.New("cleanup image metadata is invalid")
	}
	prefix := fixedProjectName + "-rollback-" + string(service) + ":"
	if !strings.HasPrefix(image.RollbackAlias, prefix) {
		return errors.New("cleanup rollback alias is invalid")
	}
	taskID := strings.TrimPrefix(image.RollbackAlias, prefix)
	parsedTaskID, err := uuid.Parse(taskID)
	if err != nil || parsedTaskID.String() != taskID {
		return errors.New("cleanup rollback alias is invalid")
	}
	return nil
}

func cleanupPendingError() error {
	return errors.New("cleanup pending")
}

func requireContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("platform operation context is required")
	}
	return nil
}

func (platform *CLIPlatform) httpHealthy(ctx context.Context, healthURL string) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return false
	}
	response, err := platform.healthClient.Do(request)
	if err != nil || response == nil || response.Body == nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
	return response.StatusCode == http.StatusOK
}

type composeOverride struct {
	Services composeServices `yaml:"services"`
}

type composeServices struct {
	Backend composeService `yaml:"backend"`
	Web     composeService `yaml:"web"`
}

type composeService struct {
	Image      string `yaml:"image"`
	PullPolicy string `yaml:"pull_policy"`
}

func (platform *CLIPlatform) writeOverride(name string, override composeOverride) (string, error) {
	contents, err := yaml.Marshal(override)
	if err != nil {
		return "", errors.New("marshal compose override")
	}
	directory := filepath.Join(platform.config.StateDir, "overrides")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", errors.New("create compose override directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", errors.New("secure compose override directory")
	}
	temporary, err := os.CreateTemp(directory, ".override-*.tmp")
	if err != nil {
		return "", errors.New("create temporary compose override")
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return "", errors.New("secure temporary compose override")
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return "", errors.New("write temporary compose override")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", errors.New("sync temporary compose override")
	}
	if err := temporary.Close(); err != nil {
		return "", errors.New("close temporary compose override")
	}
	path := filepath.Join(directory, name)
	if err := os.Rename(temporaryPath, path); err != nil {
		return "", errors.New("replace compose override")
	}
	return path, nil
}

func (platform *CLIPlatform) composeUp(ctx context.Context, service ServiceName, overridePath string) error {
	_, err := platform.runner.Run(ctx, nil, "docker",
		"compose",
		"--project-name", fixedProjectName,
		"--env-file", platform.config.ComposeEnvFile,
		"-f", platform.config.ComposeFile,
		"-f", overridePath,
		"up", "-d", "--no-deps", "--force-recreate", string(service),
	)
	if err != nil {
		return errors.New("deploy compose service")
	}
	return nil
}

func validateTargetPair(pair ImagePair, backendRepository, webRepository string) error {
	if err := validateTargetImage(pair.Backend, backendRepository); err != nil {
		return err
	}
	if err := validateTargetImage(pair.Web, webRepository); err != nil {
		return err
	}
	if pair.Backend.Version != pair.Web.Version {
		return errors.New("target image versions do not match")
	}
	return nil
}

func validateTargetImage(image Image, repository string) error {
	if image.Repository != repository || !sha256Pattern.MatchString(image.Digest) || ValidateVersion(image.Version) != nil {
		return errors.New("target image metadata is invalid")
	}
	if image.ID != "" && !sha256Pattern.MatchString(image.ID) {
		return errors.New("target image ID is invalid")
	}
	return nil
}

type serviceMetadata struct {
	image        Image
	healthStatus string
}

type containerInspect struct {
	Image  string `json:"Image"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Health *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
}

type imageInspect struct {
	ID          string   `json:"Id"`
	RepoTags    []string `json:"RepoTags"`
	RepoDigests []string `json:"RepoDigests"`
	Config      struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

func (platform *CLIPlatform) inspectService(ctx context.Context, service ServiceName) (serviceMetadata, error) {
	repository, err := platform.repositoryFor(service)
	if err != nil {
		return serviceMetadata{}, err
	}
	return platform.inspectServiceWith(ctx, service, func(reference string) bool {
		return referenceUsesRepository(reference, repository)
	}, func(output []byte, expectedID string) (Image, error) {
		return parseImageInspect(output, repository, expectedID)
	})
}

func (platform *CLIPlatform) inspectRollbackService(ctx context.Context, service ServiceName, expected Image, taskID string) (serviceMetadata, error) {
	if err := platform.validateRollbackIdentity(service, expected, taskID); err != nil {
		return serviceMetadata{}, err
	}
	return platform.inspectServiceWith(ctx, service, func(reference string) bool {
		return reference == expected.RollbackAlias
	}, func(output []byte, expectedID string) (Image, error) {
		return parseRollbackImageInspect(output, expected, expectedID)
	})
}

func (platform *CLIPlatform) inspectServiceWith(
	ctx context.Context,
	service ServiceName,
	validReference func(string) bool,
	parseImage func([]byte, string) (Image, error),
) (serviceMetadata, error) {
	output, err := platform.runner.Run(ctx, nil, "docker", "ps", "-q",
		"--filter", "label=com.docker.compose.project="+fixedProjectName,
		"--filter", "label=com.docker.compose.service="+string(service),
	)
	if err != nil {
		return serviceMetadata{}, errors.New("discover compose service")
	}
	containerIDs := strings.Fields(string(output))
	if len(containerIDs) != 1 {
		return serviceMetadata{}, errors.New("compose service must have exactly one container")
	}

	output, err = platform.runner.Run(ctx, nil, "docker", "inspect", containerIDs[0])
	if err != nil {
		return serviceMetadata{}, errors.New("inspect compose container")
	}
	var containers []containerInspect
	if json.Unmarshal(output, &containers) != nil || len(containers) != 1 {
		return serviceMetadata{}, errors.New("invalid compose container metadata")
	}
	container := containers[0]
	if !sha256Pattern.MatchString(container.Image) ||
		container.Config.Labels["com.docker.compose.project"] != fixedProjectName ||
		container.Config.Labels["com.docker.compose.service"] != string(service) ||
		!validReference(container.Config.Image) {
		return serviceMetadata{}, errors.New("untrusted compose container metadata")
	}

	output, err = platform.runner.Run(ctx, nil, "docker", "image", "inspect", container.Image)
	if err != nil {
		return serviceMetadata{}, errors.New("inspect service image")
	}
	image, err := parseImage(output, container.Image)
	if err != nil {
		return serviceMetadata{}, err
	}
	healthStatus := ""
	if container.State.Health != nil {
		healthStatus = container.State.Health.Status
	}
	return serviceMetadata{image: image, healthStatus: healthStatus}, nil
}

func (platform *CLIPlatform) validateRollbackIdentity(service ServiceName, expected Image, taskID string) error {
	repository, err := platform.repositoryFor(service)
	if err != nil {
		return err
	}
	parsedTaskID, err := uuid.Parse(taskID)
	if err != nil || parsedTaskID.String() != taskID {
		return errors.New("rollback task identity is invalid")
	}
	wantAlias := fixedProjectName + "-rollback-" + string(service) + ":" + taskID
	if expected.Repository != repository || expected.RollbackAlias != wantAlias ||
		!sha256Pattern.MatchString(expected.ID) || !sha256Pattern.MatchString(expected.Digest) ||
		ValidateVersion(expected.Version) != nil {
		return errors.New("rollback image identity is invalid")
	}
	return nil
}

func parseImageInspect(output []byte, repository, expectedID string) (Image, error) {
	var images []imageInspect
	if json.Unmarshal(output, &images) != nil || len(images) != 1 {
		return Image{}, errors.New("invalid service image metadata")
	}
	inspected := images[0]
	if !sha256Pattern.MatchString(inspected.ID) || expectedID != "" && inspected.ID != expectedID {
		return Image{}, errors.New("service image ID is invalid")
	}
	for _, tag := range inspected.RepoTags {
		if !referenceUsesRepository(tag, repository) {
			return Image{}, errors.New("service image repository is unexpected")
		}
	}
	var digest string
	for _, reference := range inspected.RepoDigests {
		prefix := repository + "@"
		if !strings.HasPrefix(reference, prefix) {
			return Image{}, errors.New("service image repository is unexpected")
		}
		candidate := strings.TrimPrefix(reference, prefix)
		if !sha256Pattern.MatchString(candidate) || digest != "" && digest != candidate {
			return Image{}, errors.New("service image digest is invalid")
		}
		digest = candidate
	}
	version := inspected.Config.Labels["org.opencontainers.image.version"]
	if digest == "" || ValidateVersion(version) != nil {
		return Image{}, errors.New("service image metadata is incomplete")
	}
	return Image{Repository: repository, Version: version, Digest: digest, ID: inspected.ID}, nil
}

func parseRollbackImageInspect(output []byte, expected Image, expectedID string) (Image, error) {
	var images []imageInspect
	if json.Unmarshal(output, &images) != nil || len(images) != 1 {
		return Image{}, errors.New("invalid rollback image metadata")
	}
	inspected := images[0]
	if expectedID != expected.ID || inspected.ID != expected.ID || !sha256Pattern.MatchString(inspected.ID) {
		return Image{}, errors.New("rollback image ID is invalid")
	}
	foundAlias := false
	for _, tag := range inspected.RepoTags {
		if tag == expected.RollbackAlias {
			foundAlias = true
			continue
		}
		if !referenceUsesRepository(tag, expected.Repository) {
			return Image{}, errors.New("rollback image tag is unexpected")
		}
	}
	if !foundAlias {
		return Image{}, errors.New("rollback image alias is missing")
	}
	wantDigest := expected.Repository + "@" + expected.Digest
	foundDigest := false
	for _, reference := range inspected.RepoDigests {
		prefix := expected.Repository + "@"
		if !strings.HasPrefix(reference, prefix) || !sha256Pattern.MatchString(strings.TrimPrefix(reference, prefix)) {
			return Image{}, errors.New("rollback image digest is unexpected")
		}
		if reference == wantDigest {
			foundDigest = true
		}
	}
	if !foundDigest || inspected.Config.Labels["org.opencontainers.image.version"] != expected.Version {
		return Image{}, errors.New("rollback image metadata does not match persisted identity")
	}
	return expected, nil
}

func (platform *CLIPlatform) repositoryFor(service ServiceName) (string, error) {
	if platform == nil {
		return "", errors.New("platform is not configured")
	}
	switch service {
	case ServiceBackend:
		return platform.config.BackendRepository, nil
	case ServiceWeb:
		return platform.config.WebRepository, nil
	default:
		return "", errors.New("service is not managed")
	}
}

func referenceUsesRepository(reference, repository string) bool {
	return strings.HasPrefix(reference, repository+":") || strings.HasPrefix(reference, repository+"@")
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
