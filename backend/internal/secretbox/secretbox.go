// Package secretbox 用 AES-256-GCM 加密需要落库的密钥（API Token、RSA 私钥等）。
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"strings"
)

const (
	envMasterKey = "APP_MASTER_KEY"
	prefix       = "v1:"
	keyLen       = 32
)

var (
	ErrNoMasterKey  = errors.New("secretbox: APP_MASTER_KEY 未配置")
	ErrBadMasterKey = errors.New("secretbox: APP_MASTER_KEY 必须是 base64 编码的 32 字节")
	ErrDecrypt      = errors.New("secretbox: 解密失败")
)

// LoadKey 从环境变量 APP_MASTER_KEY 读取主密钥。
func LoadKey() ([]byte, error) {
	v := strings.TrimSpace(os.Getenv(envMasterKey))
	if v == "" {
		return nil, ErrNoMasterKey
	}
	key, err := base64.StdEncoding.DecodeString(v)
	if err != nil || len(key) != keyLen {
		return nil, ErrBadMasterKey
	}
	return key, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal 加密 plaintext，name 作为 AAD 绑定密文用途。
func Seal(key []byte, name, plaintext string) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, []byte(plaintext), []byte(name))
	return prefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open 解密 Seal 的输出；任何不符一律返回 ErrDecrypt。
func Open(key []byte, name, sealed string) (string, error) {
	if !strings.HasPrefix(sealed, prefix) {
		return "", ErrDecrypt
	}
	raw, err := base64.StdEncoding.DecodeString(sealed[len(prefix):])
	if err != nil {
		return "", ErrDecrypt
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", ErrDecrypt
	}
	if len(raw) < gcm.NonceSize()+gcm.Overhead() {
		return "", ErrDecrypt
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, []byte(name))
	if err != nil {
		return "", ErrDecrypt
	}
	return string(pt), nil
}
