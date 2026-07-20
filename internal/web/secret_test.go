package web

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLoadOrCreateSessionKeyConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.key")
	const workers = 32
	keys := make(chan []byte, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key, err := loadOrCreateSessionKey(path)
			keys <- key
			errs <- err
		}()
	}
	wg.Wait()
	close(keys)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first []byte
	for key := range keys {
		if first == nil {
			first = key
			continue
		}
		if !bytes.Equal(key, first) {
			t.Fatal("concurrent creators returned different session keys")
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("session key permissions = %04o, want 0600", info.Mode().Perm())
	}
}
