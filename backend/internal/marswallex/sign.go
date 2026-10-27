// Package marswallex 提供 MarsWallex 虚拟卡平台的签名、验签与 API 客户端能力。
package marswallex

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SigningPayload 拼装签名原文：method&uri&epoch&body[&QueryEscape(rawQuery)]。
// rawQuery 是实际发送的查询串，签名时整串再转义一次。
func SigningPayload(method, uri string, epoch string, body []byte, rawQuery string) string {
	p := method + "&" + uri + "&" + epoch + "&" + string(body)
	if rawQuery != "" {
		p += "&" + url.QueryEscape(rawQuery)
	}
	return p
}

// Sign 使用 SHA256withRSA（PKCS#1 v1.5）签名，返回标准 base64。
func Sign(priv *rsa.PrivateKey, payload string) (string, error) {
	if priv == nil {
		return "", errors.New("marswallex: 私钥为空")
	}
	h := sha256.Sum256([]byte(payload))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, h[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// Verify 验证 base64 签名。
func Verify(pub *rsa.PublicKey, payload, sigB64 string) error {
	if pub == nil {
		return errors.New("marswallex: 公钥为空")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("marswallex: 签名 base64 无效: %w", err)
	}
	h := sha256.Sum256([]byte(payload))
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], sig)
}

// FormatSignatureHeader 生成 F-Signature 头：t=<epoch>,v=<sig>。
func FormatSignatureHeader(epoch string, sig string) string {
	return "t=" + epoch + ",v=" + sig
}

// ParseSignatureHeader 解析 F-Signature 头。每段按第一个 "=" 切分（签名本身含 "="）。
func ParseSignatureHeader(h string) (epoch, sig string, err error) {
	var hasT, hasV bool
	for _, part := range strings.Split(h, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch {
		case k == "t" && !hasT:
			epoch, hasT = v, true
		case strings.HasPrefix(k, "v") && !hasV:
			sig, hasV = v, true
		}
	}
	if !hasT || !hasV {
		return "", "", errors.New("marswallex: 签名头缺少 t 或 v")
	}
	return epoch, sig, nil
}

// EpochTime 解析时间戳：纯数字，>=13 位按毫秒，否则按秒。
func EpochTime(epoch string) (time.Time, error) {
	if epoch == "" {
		return time.Time{}, errors.New("marswallex: 时间戳为空")
	}
	for _, c := range epoch {
		if c < '0' || c > '9' {
			return time.Time{}, fmt.Errorf("marswallex: 时间戳非数字: %q", epoch)
		}
	}
	n, err := strconv.ParseInt(epoch, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("marswallex: 时间戳无效: %w", err)
	}
	if len(epoch) >= 13 {
		return time.UnixMilli(n), nil
	}
	return time.Unix(n, 0), nil
}

func decodePEM(pemText string) (*pem.Block, error) {
	s := strings.ReplaceAll(strings.TrimSpace(pemText), "\r\n", "\n")
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return nil, errors.New("marswallex: 不是有效的 PEM")
	}
	return block, nil
}

// ParsePrivateKey 解析 PKCS#8（PRIVATE KEY）或 PKCS#1（RSA PRIVATE KEY）私钥。
func ParsePrivateKey(pemText string) (*rsa.PrivateKey, error) {
	block, err := decodePEM(pemText)
	if err != nil {
		return nil, err
	}
	switch block.Type {
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("marswallex: 解析 PKCS#8 私钥失败: %w", err)
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("marswallex: 私钥不是 RSA")
		}
		return rk, nil
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("marswallex: 解析 PKCS#1 私钥失败: %w", err)
		}
		return k, nil
	}
	return nil, fmt.Errorf("marswallex: 不支持的私钥类型 %q", block.Type)
}

// ParsePublicKey 解析 PKIX（PUBLIC KEY）或 PKCS#1（RSA PUBLIC KEY）公钥。
func ParsePublicKey(pemText string) (*rsa.PublicKey, error) {
	block, err := decodePEM(pemText)
	if err != nil {
		return nil, err
	}
	switch block.Type {
	case "PUBLIC KEY":
		k, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("marswallex: 解析 PKIX 公钥失败: %w", err)
		}
		rk, ok := k.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("marswallex: 公钥不是 RSA")
		}
		return rk, nil
	case "RSA PUBLIC KEY":
		k, err := x509.ParsePKCS1PublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("marswallex: 解析 PKCS#1 公钥失败: %w", err)
		}
		return k, nil
	}
	return nil, fmt.Errorf("marswallex: 不支持的公钥类型 %q", block.Type)
}

// PublicKeyPEM 将公钥编码为 PKIX PEM。
func PublicKeyPEM(pub *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return ""
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// Fingerprint 返回 PKIX DER 的 SHA-256 小写 hex 指纹。
func Fingerprint(pub *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}
