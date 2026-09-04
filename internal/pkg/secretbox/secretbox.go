// Package secretbox provides authenticated encryption for small application secrets.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
)

const nonceSize = 12

// Box 用稳定的应用密钥加密小型 payload；密文包含 nonce，不能脱离同一密钥解开。
type Box struct {
	aead cipher.AEAD
}

// New 从配置密钥派生 AES-256 密钥。
func New(keyMaterial string) (*Box, error) {
	if keyMaterial == "" {
		return nil, errors.New("secretbox key material is empty")
	}
	key := sha256.Sum256([]byte(keyMaterial))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("create secretbox cipher: %w", err)
	}
	aead, err := cipher.NewGCMWithNonceSize(block, nonceSize)
	if err != nil {
		return nil, fmt.Errorf("create secretbox AEAD: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal 加密 payload；返回值包含 nonce 与认证标签，不含明文。
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	if b == nil || b.aead == nil {
		return nil, errors.New("secretbox is not initialized")
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate secretbox nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open 验证并解密 Seal 产生的 payload。
func (b *Box) Open(payload []byte) ([]byte, error) {
	if b == nil || b.aead == nil {
		return nil, errors.New("secretbox is not initialized")
	}
	if len(payload) < b.aead.NonceSize() {
		return nil, errors.New("secretbox payload is too short")
	}
	nonce, ciphertext := payload[:b.aead.NonceSize()], payload[b.aead.NonceSize():]
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.New("secretbox payload authentication failed")
	}
	return plaintext, nil
}
