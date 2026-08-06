package systemupdate

import (
	"context"
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
}

func (err *RegistryError) Error() string {
	return "registry image resolution failed"
}

// RegistryResolver resolves public image metadata from a container registry.
type RegistryResolver struct {
	Transport http.RoundTripper
}

// Resolve returns the linux/amd64 image metadata for the stable tag.
func (resolver *RegistryResolver) Resolve(ctx context.Context, repository, tag string) (Image, error) {
	if ctx == nil {
		return Image{}, &RegistryError{}
	}
	if tag != stableTag {
		return Image{}, &RegistryError{}
	}
	repo, err := name.NewRepository(repository, name.StrictValidation)
	if err != nil {
		return Image{}, &RegistryError{}
	}

	options := []remote.Option{remote.WithAuth(authn.Anonymous), remote.WithContext(ctx)}
	if resolver != nil && resolver.Transport != nil {
		options = append(options, remote.WithTransport(resolver.Transport))
	}
	index, err := remote.Index(repo.Tag(tag), options...)
	if err != nil {
		return Image{}, &RegistryError{}
	}
	topLevelDigest, err := index.Digest()
	if err != nil {
		return Image{}, &RegistryError{}
	}
	manifest, err := index.IndexManifest()
	if err != nil {
		return Image{}, &RegistryError{}
	}
	image, err := imageForPlatform(index, manifest, v1.Platform{OS: "linux", Architecture: "amd64"})
	if err != nil {
		return Image{}, err
	}
	config, err := image.ConfigFile()
	if err != nil {
		return Image{}, &RegistryError{}
	}
	version := config.Config.Labels["org.opencontainers.image.version"]
	if err := ValidateVersion(version); err != nil {
		return Image{}, &RegistryError{}
	}
	created, err := time.Parse(time.RFC3339, config.Config.Labels["org.opencontainers.image.created"])
	if err != nil {
		return Image{}, &RegistryError{}
	}
	return Image{Repository: repository, Version: version, Digest: topLevelDigest.String(), PublishedAt: &created}, nil
}

func imageForPlatform(index v1.ImageIndex, manifest *v1.IndexManifest, platform v1.Platform) (v1.Image, error) {
	for _, descriptor := range manifest.Manifests {
		if descriptor.Platform != nil && descriptor.Platform.Satisfies(platform) {
			image, err := index.Image(descriptor.Digest)
			if err != nil {
				return nil, &RegistryError{}
			}
			return image, nil
		}
	}
	return nil, &RegistryError{}
}
