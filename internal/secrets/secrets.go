// Package secrets: API key generation and keyed hashing, provider credential
// encryption, and random identifiers.
//
// Caller keys are stored only as an HMAC-SHA256 digest under a secret kept
// outside the database, so an exfiltrated DB is useless without it. Provider
// credentials must be recoverable (we forward them upstream), so they are
// encrypted with AES-256-GCM under a separately derived key.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const APIKeyPrefix = "lp_"

// RelayTokenPrefix distinguishes relay tokens from API keys at a glance; the
// two also live in separate tables, so neither works in the other's place.
const RelayTokenPrefix = "lpt_"

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func GenerateAPIKey() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return APIKeyPrefix + base64.RawURLEncoding.EncodeToString(b[:])
}

func GenerateRelayToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return RelayTokenPrefix + base64.RawURLEncoding.EncodeToString(b[:])
}

// GenerateSessionToken mints the opaque value of a browser session cookie.
// No prefix: it is never displayed or typed, and the base64url alphabet
// contains no '.', which is how it is told apart from the signed-cookie
// session format of earlier releases.
func GenerateSessionToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// KeySuffix returns the plaintext's last 4 characters, stored alongside the
// hash so lists can show "***xxxx" without ever keeping the key itself.
func KeySuffix(apiKey string) string {
	if len(apiKey) <= 4 {
		return apiKey
	}
	return apiKey[len(apiKey)-4:]
}

func HashAPIKey(secret []byte, apiKey string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(apiKey))
	return hex.EncodeToString(mac.Sum(nil))
}

// LoadOrCreate returns the explicit secret if set, otherwise loads (or
// creates, mode 0600) the secret file.
func LoadOrCreate(explicit, path string) ([]byte, error) {
	if explicit != "" {
		return []byte(explicit), nil
	}
	// An empty file (e.g. a crashed first run) must not become an empty key.
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		return data, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		return nil, err
	}
	return secret, nil
}

// LoadOrCreatePassword returns the explicit password if set, otherwise loads
// (or generates, mode 0600) the password file. The bool reports whether a new
// password was generated on this call.
func LoadOrCreatePassword(explicit, path string) (string, bool, error) {
	if explicit != "" {
		return explicit, false, nil
	}
	if path == "" {
		return "", false, nil
	}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		return strings.TrimSpace(string(data)), false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", false, err
	}
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", false, err
	}
	password := base64.RawURLEncoding.EncodeToString(b[:])
	if err := os.WriteFile(path, []byte(password+"\n"), 0o600); err != nil {
		return "", false, err
	}
	return password, true, nil
}

func credentialKey(secret []byte) []byte {
	sum := sha256.Sum256(append(append([]byte{}, secret...), []byte("/provider-credentials")...))
	return sum[:]
}

func EncryptCredential(secret []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(credentialKey(secret))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func DecryptCredential(secret []byte, encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(credentialKey(secret))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("credential ciphertext too short")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
