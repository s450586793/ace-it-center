// Package update provides the Agent-facing release manifest API.
package update

import (
	"crypto/ed25519"

	"aceitcenter.local/platform/internal/release"
)

const (
	ManifestSchema   = release.ManifestSchema
	ManifestChannel  = release.ManifestChannel
	MaxManifestBytes = release.MaxManifestBytes
	MaxArtifactBytes = release.MaxArtifactBytes
)

type Manifest = release.Manifest

func CanonicalPayload(manifest Manifest) ([]byte, error) {
	return release.CanonicalPayload(manifest)
}

func Sign(manifest Manifest, privateKey ed25519.PrivateKey) (Manifest, error) {
	return release.Sign(manifest, privateKey)
}

func Verify(manifest Manifest, publicKey ed25519.PublicKey) error {
	return release.Verify(manifest, publicKey)
}

func ValidateCandidate(manifest Manifest, currentVersion, currentOS, origin string) error {
	return release.ValidateCandidate(manifest, currentVersion, currentOS, origin)
}
