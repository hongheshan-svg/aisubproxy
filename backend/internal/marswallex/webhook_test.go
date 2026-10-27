package marswallex

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"testing"
	"time"
)

func whKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func whSign(t *testing.T, k *rsa.PrivateKey, epoch string, raw []byte) string {
	t.Helper()
	sig, err := Sign(k, epoch+"&"+string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return FormatSignatureHeader(epoch, sig)
}

func TestParseWebhookValid(t *testing.T) {
	k := whKey(t)
	now := time.Unix(1766123535, 0)
	raw := []byte(`{"topic":"CARD_APPLY_RESULT","message_id":"m1","data":{"card_id":"C1","status":"SUCCESS"}}`)
	h := whSign(t, k, strconv.FormatInt(now.Unix(), 10), raw)
	ev, err := ParseWebhook(h, raw, &k.PublicKey, now)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Topic != TopicCardApplyResult || ev.MessageID != "m1" {
		t.Fatalf("got %+v", ev)
	}
	var d CardApplyResultData
	if err := ev.DecodeData(&d); err != nil || d.CardID != "C1" || d.Status != "SUCCESS" {
		t.Fatalf("decode: %v %+v", err, d)
	}
}

func TestParseWebhookLegacyShape(t *testing.T) {
	k := whKey(t)
	now := time.Unix(1766123535, 0)
	raw := []byte(`{"topic":"CARD_APPLY_RESULT","messageId":"m2","data":"{\"card_id\":\"C2\"}"}`)
	h := whSign(t, k, strconv.FormatInt(now.Unix(), 10), raw)
	ev, err := ParseWebhook(h, raw, &k.PublicKey, now)
	if err != nil {
		t.Fatal(err)
	}
	if ev.MessageID != "m2" {
		t.Fatalf("id %q", ev.MessageID)
	}
	var d CardApplyResultData
	if err := ev.DecodeData(&d); err != nil || d.CardID != "C2" {
		t.Fatalf("decode: %v %+v", err, d)
	}
}

func TestParseWebhookMissingMessageIDUsesHash(t *testing.T) {
	k := whKey(t)
	now := time.Unix(1766123535, 0)
	raw := []byte(`{"topic":"CARD_STATUS_CHANGED","data":{"card_id":"C1"}}`)
	h := whSign(t, k, strconv.FormatInt(now.Unix(), 10), raw)
	ev, err := ParseWebhook(h, raw, &k.PublicKey, now)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if ev.MessageID != hex.EncodeToString(sum[:]) || len(ev.MessageID) != 64 {
		t.Fatalf("id %q", ev.MessageID)
	}
}

func TestParseWebhookRejectsBadSignature(t *testing.T) {
	k, other := whKey(t), whKey(t)
	now := time.Unix(1766123535, 0)
	epoch := strconv.FormatInt(now.Unix(), 10)
	raw := []byte(`{"topic":"CARD_APPLY_RESULT","message_id":"m1","data":{}}`)
	good := whSign(t, k, epoch, raw)
	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)-3] = 'X'
	cases := map[string]struct {
		h   string
		raw []byte
		pub *rsa.PublicKey
	}{
		"missing header": {"", raw, &k.PublicKey},
		"garbage sig":    {FormatSignatureHeader(epoch, "AAAA"), raw, &k.PublicKey},
		"wrong key":      {whSign(t, other, epoch, raw), raw, &k.PublicKey},
		"tampered body":  {good, tampered, &k.PublicKey},
		"tampered epoch": {FormatSignatureHeader(strconv.FormatInt(now.Unix()+1, 10), mustSig(t, good)), raw, &k.PublicKey},
	}
	for name, c := range cases {
		if _, err := ParseWebhook(c.h, c.raw, c.pub, now); !errors.Is(err, ErrWebhookSignature) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
}

func mustSig(t *testing.T, h string) string {
	t.Helper()
	_, s, err := ParseSignatureHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestParseWebhookTimestampWindow(t *testing.T) {
	k := whKey(t)
	now := time.Unix(1766123535, 0)
	raw := []byte(`{"topic":"CARD_APPLY_RESULT","message_id":"m1","data":{}}`)
	try := func(epoch string) error {
		_, err := ParseWebhook(whSign(t, k, epoch, raw), raw, &k.PublicKey, now)
		return err
	}
	if err := try(strconv.FormatInt(now.Add(-4*time.Minute).Unix(), 10)); err != nil {
		t.Errorf("-4m sec: %v", err)
	}
	if err := try(strconv.FormatInt(now.Add(-4*time.Minute).UnixMilli(), 10)); err != nil {
		t.Errorf("-4m ms: %v", err)
	}
	if err := try(strconv.FormatInt(now.Add(-6*time.Minute).Unix(), 10)); !errors.Is(err, ErrWebhookStale) {
		t.Errorf("-6m: %v", err)
	}
	if err := try(strconv.FormatInt(now.Add(6*time.Minute).Unix(), 10)); !errors.Is(err, ErrWebhookStale) {
		t.Errorf("+6m: %v", err)
	}
}

func TestParseWebhookBadJSON(t *testing.T) {
	k := whKey(t)
	now := time.Unix(1766123535, 0)
	raw := []byte(`not json`)
	h := whSign(t, k, strconv.FormatInt(now.Unix(), 10), raw)
	if _, err := ParseWebhook(h, raw, &k.PublicKey, now); !errors.Is(err, ErrWebhookPayload) {
		t.Fatalf("err=%v", err)
	}
}

func TestDecodeWebhookPayloadMatchesParse(t *testing.T) {
	k := whKey(t)
	now := time.Unix(1766123535, 0)
	cases := []struct {
		name string
		raw  string
	}{
		{"message_id 与对象 data", `{"topic":"CARD_APPLY_RESULT","message_id":"m1","data":{"card_id":"c1"}}`},
		{"messageId 与字符串 data", `{"topic":"CARD_STATUS_CHANGED","messageId":"m2","data":"{\"card_id\":\"c2\"}"}`},
		{"无 ID 用 sha256", `{"topic":"CARD_TRANSFER_RESULT","data":{"x":1}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := []byte(c.raw)
			want, err := ParseWebhook(whSign(t, k, strconv.FormatInt(now.Unix(), 10), raw), raw, &k.PublicKey, now)
			if err != nil {
				t.Fatalf("ParseWebhook: %v", err)
			}
			got, err := DecodeWebhookPayload(raw)
			if err != nil {
				t.Fatalf("DecodeWebhookPayload: %v", err)
			}
			if got.Topic != want.Topic || got.MessageID != want.MessageID || string(got.Data) != string(want.Data) {
				t.Errorf("got %+v, want %+v", got, want)
			}
		})
	}
	if _, err := DecodeWebhookPayload([]byte(`not json`)); !errors.Is(err, ErrWebhookPayload) {
		t.Errorf("非法 JSON: err=%v", err)
	}
}
