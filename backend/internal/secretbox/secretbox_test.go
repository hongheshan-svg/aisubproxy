package secretbox

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func testKey(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func TestSealOpenRoundTrip(t *testing.T) {
	s, err := Seal(testKey(1), "marswallex_api_token", "tok-123")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !strings.HasPrefix(s, "v1:") {
		t.Fatalf("缺少 v1: 前缀: %q", s)
	}
	if strings.Contains(s, "tok-123") {
		t.Fatalf("密文包含明文: %q", s)
	}
	got, err := Open(testKey(1), "marswallex_api_token", s)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != "tok-123" {
		t.Fatalf("got %q", got)
	}
	s2, err := Seal(testKey(1), "marswallex_api_token", "tok-123")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if s2 == s {
		t.Fatal("两次加密结果相同，随机数未生效")
	}
}

func TestOpenRejectsTamperWrongNameWrongKey(t *testing.T) {
	s, err := Seal(testKey(1), "k", "secret")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(s[3:])
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff
	tampered := "v1:" + base64.StdEncoding.EncodeToString(raw)

	cases := map[string]struct {
		key    []byte
		name   string
		sealed string
	}{
		"wrong key":    {testKey(2), "k", s},
		"wrong name":   {testKey(1), "other", s},
		"tampered":     {testKey(1), "k", tampered},
		"wrong prefix": {testKey(1), "k", "v2:" + s[3:]},
		"plain":        {testKey(1), "k", "plain"},
	}
	for label, c := range cases {
		if _, err := Open(c.key, c.name, c.sealed); !errors.Is(err, ErrDecrypt) {
			t.Errorf("%s: err = %v, want ErrDecrypt", label, err)
		}
	}
}

func TestLoadKey(t *testing.T) {
	t.Setenv("APP_MASTER_KEY", "")
	if _, err := LoadKey(); !errors.Is(err, ErrNoMasterKey) {
		t.Errorf("empty: err = %v", err)
	}
	t.Setenv("APP_MASTER_KEY", "not-base64!!")
	if _, err := LoadKey(); !errors.Is(err, ErrBadMasterKey) {
		t.Errorf("bad base64: err = %v", err)
	}
	t.Setenv("APP_MASTER_KEY", base64.StdEncoding.EncodeToString(make([]byte, 16)))
	if _, err := LoadKey(); !errors.Is(err, ErrBadMasterKey) {
		t.Errorf("short key: err = %v", err)
	}
	t.Setenv("APP_MASTER_KEY", base64.StdEncoding.EncodeToString(testKey(7)))
	got, err := LoadKey()
	if err != nil {
		t.Fatalf("LoadKey: %v", err)
	}
	if !bytes.Equal(got, testKey(7)) {
		t.Errorf("key mismatch")
	}
}
