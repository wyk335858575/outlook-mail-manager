package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

const formatVersion byte = 1

type Box struct {
	aead   cipher.AEAD
	random io.Reader
}

func New(key []byte, random io.Reader) (*Box, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secretbox key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create AES-GCM: %w", err)
	}
	if random == nil {
		random = rand.Reader
	}
	return &Box{aead: aead, random: random}, nil
}

func (b *Box) SealString(plaintext, associatedData string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(b.random, nonce); err != nil {
		return "", fmt.Errorf("generate encryption nonce: %w", err)
	}
	payload := make([]byte, 1, 1+len(nonce)+len(plaintext)+b.aead.Overhead())
	payload[0] = formatVersion
	payload = append(payload, nonce...)
	payload = b.aead.Seal(payload, nonce, []byte(plaintext), []byte(associatedData))
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func (b *Box) OpenString(encoded, associatedData string) (string, error) {
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", errors.New("encrypted value is not valid base64")
	}
	minimumLength := 1 + b.aead.NonceSize() + b.aead.Overhead()
	if len(payload) < minimumLength || payload[0] != formatVersion {
		return "", errors.New("encrypted value has an unsupported format")
	}
	nonceEnd := 1 + b.aead.NonceSize()
	plaintext, err := b.aead.Open(nil, payload[1:nonceEnd], payload[nonceEnd:], []byte(associatedData))
	if err != nil {
		return "", errors.New("encrypted value authentication failed")
	}
	return string(plaintext), nil
}
