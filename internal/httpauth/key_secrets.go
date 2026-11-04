package httpauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// loadSecretKey is called with the store lock held (or during OpenKeys).
// The companion file must travel with api-keys.json when backing up or migrating.
func (s *KeyStore) loadSecretKey(create bool) error {
	if s.secretKey != nil {
		return nil
	}
	path := s.path + ".key"
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		b = make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			b, err = os.ReadFile(path)
		} else if err == nil {
			_, err = f.Write(b)
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
			if err != nil {
				_ = os.Remove(path)
			}
		}
		if err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("read API key encryption file: %w", err)
	}
	if len(b) != 32 {
		return errors.New("invalid API key encryption file")
	}
	s.secretKey = b
	return nil
}

func (s *KeyStore) secretCipher(create bool) (cipher.AEAD, error) {
	if err := s.loadSecretKey(create); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(s.secretKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s *KeyStore) encryptSecret(id, secret string) (string, error) {
	aead, err := s.secretCipher(true)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(secret), []byte(id))
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (s *KeyStore) decryptSecret(k storedKey) (string, error) {
	if k.EncryptedSecret == "" {
		return "", ErrKeySecretUnavailable
	}
	aead, err := s.secretCipher(false)
	if err != nil {
		return "", err
	}
	sealed, err := base64.StdEncoding.DecodeString(k.EncryptedSecret)
	if err != nil || len(sealed) < aead.NonceSize()+aead.Overhead() {
		return "", errors.New("invalid encrypted API key")
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte(k.ID))
	if err != nil {
		return "", errors.New("invalid encrypted API key")
	}
	hash := sha256.Sum256(plain)
	stored, _ := hex.DecodeString(k.Hash)
	if subtle.ConstantTimeCompare(hash[:], stored) != 1 {
		return "", errors.New("API key digest mismatch")
	}
	return string(plain), nil
}

func (s *KeyStore) Reveal(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keys {
		if k.ID == id {
			return s.decryptSecret(k)
		}
	}
	return "", ErrKeyNotFound
}

// Rotate preserves the public identity, permissions and usage history; the old token stops working.
func (s *KeyStore) Rotate(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := append([]storedKey{}, s.keys...)
	for i := range keys {
		if keys[i].ID != id {
			continue
		}
		var random [32]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		secret := "sk-wb-" + hex.EncodeToString(random[:])
		sealed, err := s.encryptSecret(id, secret)
		if err != nil {
			return "", err
		}
		hash := sha256.Sum256([]byte(secret))
		keys[i].Hash = hex.EncodeToString(hash[:])
		keys[i].EncryptedSecret = sealed
		keys[i].Prefix = secret[:14]
		keys[i].SecretAvailable = true
		if err := s.save(keys); err != nil {
			return "", err
		}
		return secret, nil
	}
	return "", ErrKeyNotFound
}
