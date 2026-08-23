package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"outlook-mail-manager/internal/secretbox"
)

const (
	autoUnlockKeyFile        = "auto-unlock.key"
	autoUnlockAssociatedData = "admin:1:auto-unlock-data-key"
)

func (s *Service) AutoUnlock(ctx context.Context) (bool, error) {
	if s.autoUnlockPath == "" {
		return false, nil
	}
	var ciphertext string
	if err := s.db.QueryRowContext(ctx,
		"SELECT auto_unlock_ciphertext FROM admins WHERE id = ?", adminID,
	).Scan(&ciphertext); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("load automatic unlock metadata: %w", err)
	}
	if ciphertext == "" {
		return false, nil
	}
	nodeKey, err := readAutoUnlockKey(s.autoUnlockPath)
	if err != nil {
		return false, fmt.Errorf("read automatic unlock key: %w", err)
	}
	defer wipe(nodeKey)
	box, err := secretbox.New(nodeKey, s.random)
	if err != nil {
		return false, err
	}
	plaintext, err := box.OpenString(ciphertext, autoUnlockAssociatedData)
	if err != nil {
		return false, fmt.Errorf("open automatic unlock data key: %w", err)
	}
	dataKey := []byte(plaintext)
	defer wipe(dataKey)
	if len(dataKey) != dataKeyBytes {
		return false, errors.New("automatic unlock data key has an invalid length")
	}
	if err := s.keyring.Unlock(dataKey); err != nil {
		return false, fmt.Errorf("unlock data encryption key automatically: %w", err)
	}
	return true, nil
}

func (s *Service) sealAutoUnlock(dataKey []byte) (string, error) {
	if s.autoUnlockPath == "" {
		return "", nil
	}
	nodeKey, err := ensureAutoUnlockKey(s.autoUnlockPath, s.random)
	if err != nil {
		return "", err
	}
	defer wipe(nodeKey)
	box, err := secretbox.New(nodeKey, s.random)
	if err != nil {
		return "", err
	}
	return box.SealString(string(dataKey), autoUnlockAssociatedData)
}

func ensureAutoUnlockKey(path string, random io.Reader) ([]byte, error) {
	key, err := readAutoUnlockKey(path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errInvalidAutoUnlockKey) {
		return nil, fmt.Errorf("read automatic unlock key: %w", err)
	}
	key, err = randomBytes(random, dataKeyBytes)
	if err != nil {
		return nil, err
	}
	if err := atomicWriteAutoUnlockKey(path, key); err != nil {
		wipe(key)
		return nil, fmt.Errorf("write automatic unlock key: %w", err)
	}
	return key, nil
}

var errInvalidAutoUnlockKey = errors.New("automatic unlock key is invalid")

func readAutoUnlockKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errInvalidAutoUnlockKey
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(key) != dataKeyBytes {
		wipe(key)
		return nil, errInvalidAutoUnlockKey
	}
	if err := os.Chmod(path, 0o600); err != nil {
		wipe(key)
		return nil, err
	}
	return key, nil
}

func atomicWriteAutoUnlockKey(path string, key []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(key); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	removeTemporary = false
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
