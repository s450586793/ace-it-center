//go:build linux

package main

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestKeyReaderRejectsOversizedFIFOWithoutWaitingForEOF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("create FIFO: %v", err)
	}
	releaseWriter := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		writer, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err == nil {
			_, _ = writer.Write(bytes.Repeat([]byte("A"), 1024))
			<-releaseWriter
			_ = writer.Close()
		}
	}()
	result := make(chan error, 1)
	go func() {
		_, err := loadEncodedKey(path, ed25519.PrivateKeySize)
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("loadEncodedKey accepted oversized FIFO")
		}
		close(releaseWriter)
		<-writerDone
	case <-time.After(750 * time.Millisecond):
		close(releaseWriter)
		<-writerDone
		<-result
		t.Fatal("loadEncodedKey waited for FIFO EOF after the input exceeded its bound")
	}
}
