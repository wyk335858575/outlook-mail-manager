package datakey

import (
	"crypto/rand"
	"errors"
	"io"
	"sync"

	"outlook-mail-manager/internal/secretbox"
)

var ErrLocked = errors.New("data encryption key is locked")

type Store struct {
	mu        sync.RWMutex
	box       *secretbox.Box
	random    io.Reader
	callbacks []func()
}

func New(random io.Reader) *Store {
	if random == nil {
		random = rand.Reader
	}
	return &Store{random: random}
}

func (s *Store) Unlock(key []byte) error {
	box, err := secretbox.New(key, s.random)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.box = box
	callbacks := append([]func(){}, s.callbacks...)
	s.mu.Unlock()
	for _, callback := range callbacks {
		callback()
	}
	return nil
}

func (s *Store) OnUnlock(callback func()) {
	if callback == nil {
		return
	}
	s.mu.Lock()
	s.callbacks = append(s.callbacks, callback)
	unlocked := s.box != nil
	s.mu.Unlock()
	if unlocked {
		callback()
	}
}

func (s *Store) Locked() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.box == nil
}

func (s *Store) SealString(plaintext, associatedData string) (string, error) {
	box, err := s.current()
	if err != nil {
		return "", err
	}
	return box.SealString(plaintext, associatedData)
}

func (s *Store) OpenString(ciphertext, associatedData string) (string, error) {
	box, err := s.current()
	if err != nil {
		return "", err
	}
	return box.OpenString(ciphertext, associatedData)
}

func (s *Store) current() (*secretbox.Box, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.box == nil {
		return nil, ErrLocked
	}
	return s.box, nil
}
