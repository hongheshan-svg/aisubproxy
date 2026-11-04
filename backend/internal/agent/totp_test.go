package agent_test

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/agent"
)

func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	cases := map[int64]string{59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924", 2000000000: "279037", 20000000000: "353130"}
	for ts, want := range cases {
		if got := agent.TOTPCode(secret, ts/30); got != want {
			t.Fatalf("T=%d got %s want %s", ts, got, want)
		}
	}
}

func TestVerifyTOTPWindowAndReplay(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1700000000, 0)
	cur := agent.TOTPStep(now)
	for _, d := range []int64{-1, 0, 1} {
		step, ok := agent.VerifyTOTP(secret, agent.TOTPCode(secret, cur+d), now, 0)
		if !ok || step != cur+d {
			t.Fatalf("偏移 %d 应通过并返回对应 step，got %d %v", d, step, ok)
		}
	}
	for _, d := range []int64{-2, 2} {
		if _, ok := agent.VerifyTOTP(secret, agent.TOTPCode(secret, cur+d), now, 0); ok {
			t.Fatalf("偏移 %d 应被拒绝", d)
		}
	}
	if _, ok := agent.VerifyTOTP(secret, agent.TOTPCode(secret, cur), now, cur); ok {
		t.Fatal("lastStep 等于匹配 step 时应拒绝")
	}
	if _, ok := agent.VerifyTOTP(secret, agent.TOTPCode(secret, cur-1), now, cur); ok {
		t.Fatal("更早的时间窗应拒绝")
	}
	for _, bad := range []string{"12345", "abcdef", "", "1234567"} {
		if _, ok := agent.VerifyTOTP(secret, bad, now, 0); ok {
			t.Fatalf("%q 应拒绝", bad)
		}
	}
}

func TestSecretEncodeDecode(t *testing.T) {
	s, err := agent.NewTOTPSecret()
	if err != nil || len(s) != 20 {
		t.Fatalf("len=%d err=%v", len(s), err)
	}
	enc := agent.EncodeSecret(s)
	if strings.Contains(enc, "=") {
		t.Fatal("不应带填充")
	}
	dec, err := agent.DecodeSecret(enc)
	if err != nil || string(dec) != string(s) {
		t.Fatal("往返不一致")
	}
}

func TestOtpauthURIFormat(t *testing.T) {
	secret := []byte("12345678901234567890")
	got := agent.OtpauthURI("My App", "a@b.c", secret)
	want := "otpauth://totp/My%20App:a@b.c?secret=" + agent.EncodeSecret(secret) + "&issuer=My%20App&algorithm=SHA1&digits=6&period=30"
	u, err := url.Parse(got)
	if err != nil || u.Scheme != "otpauth" || u.Host != "totp" {
		t.Fatalf("格式不对: %s", got)
	}
	q := u.Query()
	if q.Get("secret") != agent.EncodeSecret(secret) || q.Get("issuer") != "My App" || q.Get("algorithm") != "SHA1" || q.Get("digits") != "6" || q.Get("period") != "30" {
		t.Fatalf("参数不对: %s", got)
	}
	if !strings.HasPrefix(got, "otpauth://totp/") || !strings.Contains(got, "&algorithm=SHA1&digits=6&period=30") {
		t.Fatalf("got %s want like %s", got, want)
	}
}
