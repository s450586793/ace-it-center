package systemupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxStateBytes = 1 << 20

// FileStore persists system update state at one filesystem path.
type FileStore struct {
	path string
}

type fileStoreOperations struct {
	mkdirAll   func(string, os.FileMode) error
	createTemp func(string, string) (*os.File, error)
	remove     func(string) error
	rename     func(string, string) error
	open       func(string) (*os.File, error)
}

var fileStoreOps = fileStoreOperations{
	mkdirAll:   os.MkdirAll,
	createTemp: os.CreateTemp,
	remove:     os.Remove,
	rename:     os.Rename,
	open:       os.Open,
}

// NewFileStore returns a state store that reads and writes path.
func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

// Load returns an empty state when the state file does not exist.
func (store *FileStore) Load() (PersistentState, error) {
	file, err := fileStoreOps.open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return PersistentState{}, nil
	}
	if err != nil {
		return PersistentState{}, fmt.Errorf("open update state: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return PersistentState{}, fmt.Errorf("stat update state: %w", err)
	}
	if info.Size() > maxStateBytes {
		return PersistentState{}, fmt.Errorf("update state exceeds %d bytes", maxStateBytes)
	}

	decoder := json.NewDecoder(io.LimitReader(file, maxStateBytes+1))
	decoder.DisallowUnknownFields()
	var state PersistentState
	if err := decoder.Decode(&state); err != nil {
		return PersistentState{}, fmt.Errorf("decode update state: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return PersistentState{}, errors.New("decode update state: multiple JSON values")
		}
		return PersistentState{}, fmt.Errorf("decode update state: %w", err)
	}
	return state, nil
}

// Save atomically replaces the state file after persisting its contents.
func (store *FileStore) Save(state PersistentState) error {
	directory := filepath.Dir(store.path)
	if err := fileStoreOps.mkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create update state directory: %w", err)
	}

	temporary, err := fileStoreOps.createTemp(directory, ".systemupdate-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary update state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = fileStoreOps.remove(temporaryPath) }()

	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set temporary update state permissions: %w", err)
	}
	if err := json.NewEncoder(temporary).Encode(state); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode update state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary update state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary update state: %w", err)
	}
	if err := fileStoreOps.rename(temporaryPath, store.path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	if err := syncStateDirectory(directory); err != nil {
		return err
	}
	return nil
}

func syncStateDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open update state directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync update state directory: %w", err)
	}
	return nil
}
