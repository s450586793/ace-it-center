package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aceitcenter.local/platform/internal/release"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeUsage(stderr)
		return 2
	}
	var err error
	var usageError bool
	switch args[0] {
	case "keygen":
		err, usageError = runKeygen(args[1:], stderr)
	case "public-key":
		err, usageError = runPublicKey(args[1:], stdout, stderr)
	case "sign":
		err, usageError = runSign(args[1:], stderr)
	case "verify":
		err, usageError = runVerify(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "ace-release: unknown command")
		writeUsage(stderr)
		return 2
	}
	if err == nil {
		return 0
	}
	fmt.Fprintf(stderr, "ace-release: %v\n", err)
	if usageError {
		return 2
	}
	return 1
}

func writeUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: ace-release <keygen|public-key|sign|verify> [options]")
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags
}

func runKeygen(args []string, stderr io.Writer) (error, bool) {
	flags := newFlagSet("keygen", stderr)
	privatePath := flags.String("private", "", "private key output path")
	publicPath := flags.String("public", "", "public key output path")
	if err := flags.Parse(args); err != nil {
		return err, true
	}
	if flags.NArg() != 0 || *privatePath == "" || *publicPath == "" {
		return errors.New("keygen requires -private and -public"), true
	}
	if err := generateKeyPair(*privatePath, *publicPath); err != nil {
		return err, false
	}
	return nil, false
}

func runPublicKey(args []string, stdout, stderr io.Writer) (error, bool) {
	flags := newFlagSet("public-key", stderr)
	privatePath := flags.String("private", "", "private key input path")
	if err := flags.Parse(args); err != nil {
		return err, true
	}
	if flags.NArg() != 0 || *privatePath == "" {
		return errors.New("public-key requires -private"), true
	}
	privateKey, err := readPrivateKey(*privatePath)
	if err != nil {
		return err, false
	}
	derivedPrivate := ed25519.NewKeyFromSeed(privateKey[:ed25519.SeedSize])
	publicKey := derivedPrivate.Public().(ed25519.PublicKey)
	if _, err := fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(publicKey)); err != nil {
		return errors.New("write public key"), false
	}
	return nil, false
}

func runSign(args []string, stderr io.Writer) (error, bool) {
	flags := newFlagSet("sign", stderr)
	privatePath := flags.String("private", "", "private key input path")
	artifactPath := flags.String("artifact", "", "installer artifact path")
	manifestPath := flags.String("manifest", "", "signed manifest output path")
	version := flags.String("version", "", "release semantic version")
	publishedAtText := flags.String("published-at", "", "UTC RFC3339 release timestamp")
	minimumOS := flags.String("minimum-os", "", "minimum Windows version")
	artifactURL := flags.String("url", "", "artifact URL")
	origin := flags.String("origin", "", "server origin for absolute artifact URLs")
	if err := flags.Parse(args); err != nil {
		return err, true
	}
	if flags.NArg() != 0 || *privatePath == "" || *artifactPath == "" || *manifestPath == "" || *version == "" || *publishedAtText == "" || *minimumOS == "" || *artifactURL == "" {
		return errors.New("sign requires -private, -artifact, -manifest, -version, -published-at, -minimum-os, and -url"), true
	}
	publishedAt, err := parseCanonicalTime(*publishedAtText)
	if err != nil {
		return err, false
	}
	size, digest, err := hashArtifact(*artifactPath)
	if err != nil {
		return err, false
	}
	manifest := release.Manifest{
		Schema:      release.ManifestSchema,
		Channel:     release.ManifestChannel,
		Version:     *version,
		PublishedAt: publishedAt,
		MinimumOS:   *minimumOS,
		URL:         *artifactURL,
		Size:        size,
		SHA256:      hex.EncodeToString(digest),
	}
	if err := validateCLIOrigin(manifest, *origin); err != nil {
		return err, false
	}
	privateKey, err := readPrivateKey(*privatePath)
	if err != nil {
		return err, false
	}
	manifest, err = release.Sign(manifest, privateKey)
	if err != nil {
		return fmt.Errorf("sign manifest: %w", err), false
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return errors.New("encode signed manifest"), false
	}
	encoded = append(encoded, '\n')
	if err := writeAtomic(*manifestPath, encoded, 0o644); err != nil {
		return fmt.Errorf("write signed manifest: %w", err), false
	}
	return nil, false
}

func runVerify(args []string, stdout, stderr io.Writer) (error, bool) {
	flags := newFlagSet("verify", stderr)
	publicPath := flags.String("public", "", "public key input path")
	artifactPath := flags.String("artifact", "", "installer artifact path")
	manifestPath := flags.String("manifest", "", "signed manifest input path")
	origin := flags.String("origin", "", "server origin for absolute artifact URLs")
	if err := flags.Parse(args); err != nil {
		return err, true
	}
	if flags.NArg() != 0 || *publicPath == "" || *artifactPath == "" || *manifestPath == "" {
		return errors.New("verify requires -public, -artifact, and -manifest"), true
	}
	manifestFile, err := os.Open(*manifestPath)
	if err != nil {
		return errors.New("open manifest"), false
	}
	manifest, decodeErr := decodeStrictManifest(manifestFile)
	closeErr := manifestFile.Close()
	if decodeErr != nil {
		return fmt.Errorf("decode manifest: %w", decodeErr), false
	}
	if closeErr != nil {
		return errors.New("close manifest"), false
	}
	if err := validateCLIOrigin(manifest, *origin); err != nil {
		return err, false
	}
	publicKey, err := readPublicKey(*publicPath)
	if err != nil {
		return err, false
	}
	if err := release.Verify(manifest, publicKey); err != nil {
		return fmt.Errorf("verify manifest: %w", err), false
	}
	size, digest, err := hashArtifact(*artifactPath)
	if err != nil {
		return err, false
	}
	wantDigest, err := hex.DecodeString(manifest.SHA256)
	if err != nil {
		return errors.New("manifest artifact hash is invalid"), false
	}
	if size != manifest.Size || subtle.ConstantTimeCompare(digest, wantDigest) != 1 {
		return errors.New("artifact size or SHA-256 does not match manifest"), false
	}
	if _, err := fmt.Fprintln(stdout, "verified"); err != nil {
		return errors.New("write verification result"), false
	}
	return nil, false
}

func parseCanonicalTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.Location() != time.UTC || parsed.Nanosecond() != 0 || parsed.Format(time.RFC3339) != value {
		return time.Time{}, errors.New("published-at must be a canonical whole-second UTC RFC3339 timestamp")
	}
	return parsed, nil
}

func validateCLIOrigin(manifest release.Manifest, origin string) error {
	parsed, err := url.Parse(manifest.URL)
	if err != nil {
		return errors.New("manifest URL is invalid")
	}
	if origin == "" {
		if parsed.IsAbs() {
			return errors.New("origin is required for an absolute manifest URL")
		}
		_, err := release.CanonicalPayload(manifest)
		return err
	}
	return release.ValidateManifestOrigin(manifest, origin)
}

func hashArtifact(path string) (int64, []byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, nil, errors.New("open artifact")
	}
	defer file.Close()
	digest := sha256.New()
	size, err := copyBounded(digest, file, release.MaxArtifactBytes)
	if err != nil {
		return 0, nil, err
	}
	if size == 0 {
		return 0, nil, errors.New("artifact must not be empty")
	}
	return size, digest.Sum(nil), nil
}

func copyBounded(destination hash.Hash, source io.Reader, maximum int64) (int64, error) {
	written, err := io.Copy(destination, io.LimitReader(source, maximum+1))
	if err != nil {
		return 0, errors.New("read artifact")
	}
	if written > maximum {
		return 0, errors.New("artifact exceeds maximum size")
	}
	return written, nil
}

type keyFileOperations struct {
	lstat         func(string) (os.FileInfo, error)
	createTemp    func(string, string) (keyTemporaryFile, error)
	link          func(string, string) error
	remove        func(string) error
	syncDirectory func(string) error
}

type keyTemporaryFile interface {
	Name() string
	Chmod(os.FileMode) error
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

var keyOperations = keyFileOperations{
	lstat: os.Lstat,
	createTemp: func(directory, pattern string) (keyTemporaryFile, error) {
		return os.CreateTemp(directory, pattern)
	},
	link:          os.Link,
	remove:        os.Remove,
	syncDirectory: syncDirectory,
}

var errKeyRollbackIncomplete = errors.New("rollback was incomplete")

type ownedKeyFile struct {
	path      string
	directory string
	handle    keyTemporaryFile
	exists    bool
}

type keyGenerationTransaction struct {
	operations  keyFileOperations
	owned       []*ownedKeyFile
	affected    map[string]struct{}
	directories []string
}

func newKeyGenerationTransaction(privatePath, publicPath string) *keyGenerationTransaction {
	return &keyGenerationTransaction{
		operations:  keyOperations,
		affected:    make(map[string]struct{}, 2),
		directories: []string{filepath.Dir(publicPath), filepath.Dir(privatePath)},
	}
}

func (transaction *keyGenerationTransaction) prepare(destination string, contents []byte, mode os.FileMode) (*ownedKeyFile, error) {
	directory := filepath.Dir(destination)
	file, err := transaction.operations.createTemp(directory, ".ace-release-*.tmp")
	if err != nil {
		return nil, errors.New("create temporary key")
	}
	owned := &ownedKeyFile{path: file.Name(), directory: directory, handle: file, exists: true}
	transaction.owned = append(transaction.owned, owned)
	transaction.markAffected(directory)
	if err := file.Chmod(mode); err != nil {
		return nil, errors.New("set temporary key permissions")
	}
	written, err := file.Write(contents)
	if err != nil || written != len(contents) {
		return nil, errors.New("write temporary key")
	}
	if err := file.Sync(); err != nil {
		return nil, errors.New("sync temporary key")
	}
	if err := file.Close(); err != nil {
		return nil, errors.New("close temporary key")
	}
	owned.handle = nil
	return owned, nil
}

func (transaction *keyGenerationTransaction) publish(temporary *ownedKeyFile, destination string) (*ownedKeyFile, error) {
	if err := transaction.operations.link(temporary.path, destination); err != nil {
		return nil, errors.New("publish key without overwrite")
	}
	published := &ownedKeyFile{path: destination, directory: filepath.Dir(destination), exists: true}
	transaction.owned = append(transaction.owned, published)
	transaction.markAffected(published.directory)
	return published, nil
}

func (transaction *keyGenerationTransaction) remove(owned *ownedKeyFile) error {
	if owned == nil || !owned.exists {
		return nil
	}
	if owned.handle != nil {
		_ = owned.handle.Close()
		owned.handle = nil
	}
	transaction.markAffected(owned.directory)
	if err := transaction.operations.remove(owned.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("remove owned key file")
	}
	owned.exists = false
	return nil
}

func (transaction *keyGenerationTransaction) sync(directory string) error {
	if err := transaction.operations.syncDirectory(directory); err != nil {
		return errors.New("sync key directory")
	}
	delete(transaction.affected, filepath.Clean(directory))
	return nil
}

func (transaction *keyGenerationTransaction) markAffected(directory string) {
	transaction.affected[filepath.Clean(directory)] = struct{}{}
}

func (transaction *keyGenerationTransaction) rollback() error {
	incomplete := false
	for index := len(transaction.owned) - 1; index >= 0; index-- {
		if err := transaction.remove(transaction.owned[index]); err != nil {
			incomplete = true
		}
	}
	seen := make(map[string]struct{}, len(transaction.directories))
	for _, directory := range transaction.directories {
		cleaned := filepath.Clean(directory)
		if _, duplicate := seen[cleaned]; duplicate {
			continue
		}
		seen[cleaned] = struct{}{}
		if _, affected := transaction.affected[cleaned]; !affected {
			continue
		}
		if err := transaction.sync(directory); err != nil {
			incomplete = true
		}
	}
	if incomplete {
		return errKeyRollbackIncomplete
	}
	return nil
}

func (transaction *keyGenerationTransaction) fail(primary error) error {
	if err := transaction.rollback(); err != nil {
		return errKeyRollbackIncomplete
	}
	return primary
}

func (transaction *keyGenerationTransaction) cleanupFailure() error {
	_ = transaction.rollback()
	return errKeyRollbackIncomplete
}

func generateKeyPair(privatePath, publicPath string) error {
	privateAbsolute, err := filepath.Abs(privatePath)
	if err != nil {
		return errors.New("resolve private key path")
	}
	publicAbsolute, err := filepath.Abs(publicPath)
	if err != nil {
		return errors.New("resolve public key path")
	}
	if privateAbsolute == publicAbsolute {
		return errors.New("private and public key paths must differ")
	}
	for _, path := range []string{privatePath, publicPath} {
		if _, err := keyOperations.lstat(path); err == nil {
			return errors.New("key destination already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("inspect key destination")
		}
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return errors.New("generate Ed25519 key")
	}
	privateDirectory := filepath.Dir(privatePath)
	publicDirectory := filepath.Dir(publicPath)
	transaction := newKeyGenerationTransaction(privatePath, publicPath)
	privateTemporary, err := transaction.prepare(privatePath, encodeKey(privateKey), 0o600)
	if err != nil {
		return transaction.fail(err)
	}
	publicTemporary, err := transaction.prepare(publicPath, encodeKey(publicKey), 0o644)
	if err != nil {
		return transaction.fail(err)
	}
	if _, err := transaction.publish(publicTemporary, publicPath); err != nil {
		return transaction.fail(err)
	}
	if err := transaction.remove(publicTemporary); err != nil {
		return transaction.cleanupFailure()
	}
	if err := transaction.sync(publicDirectory); err != nil {
		return transaction.fail(err)
	}
	if _, err := transaction.publish(privateTemporary, privatePath); err != nil {
		return transaction.fail(err)
	}
	if err := transaction.remove(privateTemporary); err != nil {
		return transaction.cleanupFailure()
	}
	if err := transaction.sync(privateDirectory); err != nil {
		return transaction.fail(err)
	}
	return nil
}

func encodeKey(key []byte) []byte {
	return append([]byte(base64.StdEncoding.EncodeToString(key)), '\n')
}

func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	key, err := loadEncodedKey(path, ed25519.PrivateKeySize)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	derived := ed25519.NewKeyFromSeed(key[:ed25519.SeedSize])
	if subtle.ConstantTimeCompare(key, derived) != 1 {
		return nil, errors.New("read private key: key file contains inconsistent Ed25519 key material")
	}
	return derived, nil
}

func readPublicKey(path string) (ed25519.PublicKey, error) {
	key, err := loadEncodedKey(path, ed25519.PublicKeySize)
	if err != nil {
		return nil, fmt.Errorf("read public key: %w", err)
	}
	return ed25519.PublicKey(key), nil
}

func loadEncodedKey(path string, expectedLength int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("open key file")
	}
	encodedLength := base64.StdEncoding.EncodedLen(expectedLength)
	maximumFileBytes := encodedLength + 1
	contents, readErr := io.ReadAll(io.LimitReader(file, int64(maximumFileBytes)+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, errors.New("read key file")
	}
	if closeErr != nil {
		return nil, errors.New("close key file")
	}
	if len(contents) != maximumFileBytes || contents[len(contents)-1] != '\n' {
		return nil, errors.New("key file is not canonical base64")
	}
	encoded := string(contents[:encodedLength])
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(key) != expectedLength || base64.StdEncoding.EncodeToString(key) != encoded {
		return nil, errors.New("key file contains invalid Ed25519 key material")
	}
	return key, nil
}

func writeTemporary(destination string, contents []byte, mode os.FileMode) (string, error) {
	directory := filepath.Dir(destination)
	temporary, err := os.CreateTemp(directory, ".ace-release-*.tmp")
	if err != nil {
		return "", errors.New("create temporary file")
	}
	path := temporary.Name()
	completed := false
	defer func() {
		if !completed {
			temporary.Close()
			os.Remove(path)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return "", errors.New("set temporary file permissions")
	}
	if _, err := temporary.Write(contents); err != nil {
		return "", errors.New("write temporary file")
	}
	if err := temporary.Sync(); err != nil {
		return "", errors.New("sync temporary file")
	}
	if err := temporary.Close(); err != nil {
		return "", errors.New("close temporary file")
	}
	completed = true
	return path, nil
}

func writeAtomic(destination string, contents []byte, mode os.FileMode) error {
	temporary, err := writeTemporary(destination, contents, mode)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Rename(temporary, destination); err != nil {
		return errors.New("replace destination")
	}
	return syncDirectory(filepath.Dir(destination))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return errors.New("open output directory")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("sync output directory")
	}
	return nil
}

func decodeStrictManifest(reader io.Reader) (release.Manifest, error) {
	contents, err := io.ReadAll(io.LimitReader(reader, release.MaxManifestBytes+1))
	if err != nil {
		return release.Manifest{}, errors.New("read manifest")
	}
	if len(contents) > release.MaxManifestBytes {
		return release.Manifest{}, errors.New("manifest exceeds maximum size")
	}
	if err := validateManifestJSONTokens(contents); err != nil {
		return release.Manifest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var manifest release.Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return release.Manifest{}, errors.New("manifest JSON is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return release.Manifest{}, errors.New("manifest contains trailing JSON data")
	}
	var timestamp struct {
		PublishedAt string `json:"published_at"`
	}
	if err := json.Unmarshal(contents, &timestamp); err != nil || timestamp.PublishedAt != manifest.PublishedAt.Format(time.RFC3339) {
		return release.Manifest{}, errors.New("manifest published_at is not canonical UTC RFC3339")
	}
	return manifest, nil
}

var manifestJSONFields = map[string]struct{}{
	"schema": {}, "channel": {}, "version": {}, "published_at": {}, "minimum_os": {},
	"url": {}, "size": {}, "sha256": {}, "signature": {},
}

func validateManifestJSONTokens(contents []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	token, err := decoder.Token()
	if err != nil {
		return errors.New("manifest JSON is invalid")
	}
	opening, ok := token.(json.Delim)
	if !ok || opening != '{' {
		return errors.New("manifest JSON root must be an object")
	}
	seenExact := make(map[string]struct{}, len(manifestJSONFields))
	seenFolded := make(map[string]struct{}, len(manifestJSONFields))
	hasUnknown := false
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return errors.New("manifest JSON is invalid")
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("manifest JSON object key is invalid")
		}
		folded := strings.ToLower(key)
		if _, exists := seenFolded[folded]; exists {
			return errors.New("manifest contains a duplicate JSON field")
		}
		seenFolded[folded] = struct{}{}
		if _, allowed := manifestJSONFields[key]; !allowed {
			hasUnknown = true
		} else {
			seenExact[key] = struct{}{}
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return errors.New("manifest JSON is invalid")
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return errors.New("manifest JSON is invalid")
	}
	if hasUnknown {
		return errors.New("manifest contains an unknown JSON field")
	}
	if len(seenExact) != len(manifestJSONFields) {
		return errors.New("manifest JSON fields are incomplete")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("manifest contains trailing JSON data")
	}
	return nil
}
