package web

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const sessionKeyBytes = 32

func loadOrCreateSessionKey(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return randomSessionKey()
	}
	key, err := readSessionKey(path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	key, err = randomSessionKey()
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return nil, err
	}
	tmp := file.Name()
	defer func() {
		_ = file.Close()
		_ = os.Remove(tmp)
	}()
	if err := file.Chmod(0600); err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(file, "%s\n", hex.EncodeToString(key)); err != nil {
		return nil, err
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return readSessionKey(path)
		}
		return nil, err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return nil, err
	}
	if err := directory.Close(); err != nil {
		return nil, err
	}
	return key, nil
}

func readSessionKey(path string) ([]byte, error) {
	data, err := readPrivateFile(path, "web session key", sessionKeyBytes*2+1)
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(key) != sessionKeyBytes {
		return nil, fmt.Errorf("web session key must contain exactly %d random bytes encoded as hex", sessionKeyBytes)
	}
	return key, nil
}

func randomSessionKey() ([]byte, error) {
	key := make([]byte, sessionKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}
