// Package agent 是代理商相关的纯逻辑：两步验证、会话令牌、名称与编号校验。不碰数据库。
package agent

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret 生成 20 字节随机密钥。
func NewTOTPSecret() ([]byte, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// EncodeSecret 把密钥编码成不带填充的 base32。
func EncodeSecret(b []byte) string { return b32.EncodeToString(b) }

// DecodeSecret 解码 base32 密钥（容忍小写和空格）。
func DecodeSecret(s string) ([]byte, error) {
	s = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	s = strings.TrimRight(s, "=")
	return b32.DecodeString(s)
}

// TOTPStep 返回时间对应的 30 秒时间窗编号。
func TOTPStep(t time.Time) int64 { return t.Unix() / 30 }

// TOTPCode 按 RFC 6238（HMAC-SHA1，6 位）计算动态码。
func TOTPCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1000000)
}

// VerifyTOTP 在 step-1、step、step+1 三个时间窗里找匹配，只接受大于 lastStep 的窗。
// 三个窗都会比较完再返回；返回匹配上的时间窗。
func VerifyTOTP(secret []byte, code string, now time.Time, lastStep int64) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	cur := TOTPStep(now)
	var matched int64
	found := false
	for _, s := range []int64{cur - 1, cur, cur + 1} {
		if subtle.ConstantTimeCompare([]byte(TOTPCode(secret, s)), []byte(code)) == 1 && s > lastStep && !found {
			matched, found = s, true
		}
	}
	return matched, found
}

// OtpauthURI 生成给验证器扫码的 otpauth 地址。
func OtpauthURI(issuer, account string, secret []byte) string {
	iss := url.PathEscape(issuer)
	return "otpauth://totp/" + iss + ":" + url.PathEscape(account) +
		"?secret=" + EncodeSecret(secret) + "&issuer=" + iss +
		"&algorithm=SHA1&digits=6&period=30"
}
