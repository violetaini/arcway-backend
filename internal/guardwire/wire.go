package guardwire

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	HeaderTimestamp = "X-Arcway-Guard-Timestamp"
	HeaderNonce     = "X-Arcway-Guard-Nonce"
	HeaderSignature = "X-Arcway-Guard-Signature"
	MaxClockSkew    = 2 * time.Minute
)

type Metadata struct {
	Timestamp string
	Nonce     string
	Signature string
}

func derive(secret, purpose string) [32]byte {
	return sha256.Sum256([]byte("arcway-expiry-guard-v1\x00" + purpose + "\x00" + secret))
}

func canonical(method, path, timestamp, nonce string, body []byte) []byte {
	digest := sha256.Sum256(body)
	return []byte(strings.ToUpper(method) + "\n" + path + "\n" + timestamp + "\n" + nonce + "\n" + base64.RawURLEncoding.EncodeToString(digest[:]))
}

func aad(method, path, timestamp, nonce string) []byte {
	return []byte(strings.ToUpper(method) + "\n" + path + "\n" + timestamp + "\n" + nonce)
}

func Seal(secret, method, path string, plaintext []byte, now time.Time) ([]byte, Metadata, error) {
	if strings.TrimSpace(secret) == "" || method == "" || path == "" {
		return nil, Metadata{}, errors.New("guard secret, method, and path are required")
	}
	timestamp := strconv.FormatInt(now.UTC().Unix(), 10)
	requestNonce := make([]byte, 18)
	if _, err := rand.Read(requestNonce); err != nil {
		return nil, Metadata{}, fmt.Errorf("generate request nonce: %w", err)
	}
	nonceHeader := base64.RawURLEncoding.EncodeToString(requestNonce)
	key := derive(secret, "encryption")
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, Metadata{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, Metadata{}, err
	}
	messageNonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(messageNonce); err != nil {
		return nil, Metadata{}, fmt.Errorf("generate message nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, messageNonce, plaintext, aad(method, path, timestamp, nonceHeader))
	body := append(messageNonce, ciphertext...)
	authKey := derive(secret, "authentication")
	mac := hmac.New(sha256.New, authKey[:])
	_, _ = mac.Write(canonical(method, path, timestamp, nonceHeader, body))
	return body, Metadata{
		Timestamp: timestamp,
		Nonce:     nonceHeader,
		Signature: base64.RawURLEncoding.EncodeToString(mac.Sum(nil)),
	}, nil
}

func Open(secret, method, path string, body []byte, metadata Metadata, now time.Time) ([]byte, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("guard secret is required")
	}
	unix, err := strconv.ParseInt(metadata.Timestamp, 10, 64)
	if err != nil {
		return nil, errors.New("invalid guard timestamp")
	}
	delta := now.UTC().Sub(time.Unix(unix, 0).UTC())
	if delta < -MaxClockSkew || delta > MaxClockSkew {
		return nil, errors.New("guard request timestamp is outside the allowed window")
	}
	requestNonce, err := base64.RawURLEncoding.DecodeString(metadata.Nonce)
	if err != nil || len(requestNonce) < 16 {
		return nil, errors.New("invalid guard request nonce")
	}
	providedSignature, err := base64.RawURLEncoding.DecodeString(metadata.Signature)
	if err != nil || len(providedSignature) != sha256.Size {
		return nil, errors.New("invalid guard request signature")
	}
	authKey := derive(secret, "authentication")
	mac := hmac.New(sha256.New, authKey[:])
	_, _ = mac.Write(canonical(method, path, metadata.Timestamp, metadata.Nonce, body))
	expectedSignature := mac.Sum(nil)
	if subtle.ConstantTimeCompare(providedSignature, expectedSignature) != 1 {
		return nil, errors.New("invalid guard request signature")
	}
	key := derive(secret, "encryption")
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(body) < aead.NonceSize()+aead.Overhead() {
		return nil, errors.New("invalid encrypted guard payload")
	}
	plaintext, err := aead.Open(nil, body[:aead.NonceSize()], body[aead.NonceSize():], aad(method, path, metadata.Timestamp, metadata.Nonce))
	if err != nil {
		return nil, errors.New("invalid encrypted guard payload")
	}
	return plaintext, nil
}
