package systemupdate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

const stableTag = "stable"

// ImageResolver resolves a published image from a container registry.
type ImageResolver interface {
	Resolve(ctx context.Context, repository, tag string) (Image, error)
}

// RegistryError reports a retryable error while communicating with a registry.
type RegistryError struct {
	Operation string
}

func (err *RegistryError) Error() string {
	return "registry " + err.Operation + " failed"
}

// RegistryResolver resolves public image metadata from a container registry.
type RegistryResolver struct {
	Transport http.RoundTripper
	Platform  v1.Platform
}

// Resolve returns the linux/amd64 image metadata for the stable tag.
func (resolver *RegistryResolver) Resolve(ctx context.Context, repository, tag string) (Image, error) {
	if ctx == nil {
		return Image{}, errors.New("registry context is required")
	}
	if tag != stableTag {
		return Image{}, errors.New("registry tag must be stable")
	}
	repo, err := name.NewRepository(repository, name.StrictValidation)
	if err != nil {
		return Image{}, fmt.Errorf("parse registry repository: %w", err)
	}

	options := []remote.Option{remote.WithAuth(authn.Anonymous), remote.WithContext(ctx)}
	if resolver.Transport != nil {
		options = append(options, remote.WithTransport(resolver.Transport))
	}
	index, err := remote.Index(repo.Tag(tag), options...)
	if err != nil {
		return Image{}, &RegistryError{Operation: "index lookup"}
	}
	topLevelDigest, err := index.Digest()
	if err != nil {
		return Image{}, &RegistryError{Operation: "index digest"}
	}
	manifest, err := index.IndexManifest()
	if err != nil {
		return Image{}, &RegistryError{Operation: "index manifest"}
	}
	image, err := imageForPlatform(index, manifest, resolver.platform())
	if err != nil {
		return Image{}, err
	}
	config, err := image.ConfigFile()
	if err != nil {
		return Image{}, &RegistryError{Operation: "image config"}
	}
	version := config.Config.Labels["org.opencontainers.image.version"]
	if err := ValidateVersion(version); err != nil {
		return Image{}, fmt.Errorf("validate registry image version: %w", err)
	}
	created, err := time.Parse(time.RFC3339, config.Config.Labels["org.opencontainers.image.created"])
	if err != nil {
		return Image{}, errors.New("registry image created label must be RFC3339")
	}
	return Image{Repository: repository, Version: version, Digest: topLevelDigest.String(), PublishedAt: &created}, nil
}

func (resolver *RegistryResolver) platform() v1.Platform {
	if resolver.Platform.OS != "" || resolver.Platform.Architecture != "" {
		return resolver.Platform
	}
	return v1.Platform{OS: "linux", Architecture: "amd64"}
}

func imageForPlatform(index v1.ImageIndex, manifest *v1.IndexManifest, platform v1.Platform) (v1.Image, error) {
	for _, descriptor := range manifest.Manifests {
		if descriptor.Platform != nil && descriptor.Platform.Satisfies(platform) {
			image, err := index.Image(descriptor.Digest)
			if err != nil {
				return nil, &RegistryError{Operation: "platform image"}
			}
			return image, nil
		}
	}
	return nil, errors.New("registry image does not support required platform")
}
