package marswallex

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/url"
	"strings"
	"testing"
)

func TestSigningPayloadMatchesDocExamples(t *testing.T) {
	cases := []struct {
		method, uri string
		body        []byte
		q, want     string
	}{
		{"GET", "/portal/api/v1/accounts", nil, "", "GET&/portal/api/v1/accounts&1766123535&"},
		{"POST", "/portal/api/v1/cardholder", []byte(`{"out_card_holder_id":"CH0001","first_name":"Tom"}`), "",
			`POST&/portal/api/v1/cardholder&1766123535&{"out_card_holder_id":"CH0001","first_name":"Tom"}`},
		{"POST", "/portal/api/v1/file/upload", nil, "biz_type=CARDHOLDER_ID_PHOTO",
			"POST&/portal/api/v1/file/upload&1766123535&&biz_type%3DCARDHOLDER_ID_PHOTO"},
		{"GET", "/portal/api/v1/accounts/page", nil, "page_no=1&page_size=10",
			"GET&/portal/api/v1/accounts/page&1766123535&&page_no%3D1%26page_size%3D10"},
	}
	for _, c := range cases {
		if got := SigningPayload(c.method, c.uri, "1766123535", c.body, c.q); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}

func TestSigningPayloadEncodesSpecialQueryValues(t *testing.T) {
	q := url.Values{"k": {"a b&c"}}.Encode()
	want := "GET&/x&1&&k%3Da%2Bb%2526c"
	if got := SigningPayload("GET", "/x", "1", nil, q); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestSignVerifyRoundTripAndTamper(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := Sign(priv, "GET&/x&1&")
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(&priv.PublicKey, "GET&/x&1&", sig); err != nil {
		t.Errorf("verify: %v", err)
	}
	if Verify(&priv.PublicKey, "GET&/x&2&", sig) == nil {
		t.Error("tampered payload should fail")
	}
	if Verify(&priv.PublicKey, "GET&/x&1&", "!!") == nil {
		t.Error("bad base64 should fail")
	}
}

func TestSignatureHeader(t *testing.T) {
	e, s, err := ParseSignatureHeader("t=1706498637,v=XJk/9+abc=")
	if err != nil || e != "1706498637" || s != "XJk/9+abc=" {
		t.Errorf("got %q %q %v", e, s, err)
	}
	e, s, err = ParseSignatureHeader(FormatSignatureHeader("1", "s=="))
	if err != nil || e != "1" || s != "s==" {
		t.Errorf("roundtrip got %q %q %v", e, s, err)
	}
	for _, h := range []string{"v=abc", "t=1", ""} {
		if _, _, err := ParseSignatureHeader(h); err == nil {
			t.Errorf("%q should fail", h)
		}
	}
}

func TestEpochTime(t *testing.T) {
	if tm, err := EpochTime("1766123535"); err != nil || tm.Unix() != 1766123535 {
		t.Errorf("sec: %v %v", tm, err)
	}
	if tm, err := EpochTime("1766123822742"); err != nil || tm.UnixMilli() != 1766123822742 {
		t.Errorf("ms: %v %v", tm, err)
	}
	for _, e := range []string{"12a", ""} {
		if _, err := EpochTime(e); err == nil {
			t.Errorf("%q should fail", e)
		}
	}
}

func testPEMs(t *testing.T) (priv *rsa.PrivateKey, p1, p8, pub1, pub8 string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p8der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pubder, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	p1 = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)}))
	p8 = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p8der}))
	pub1 = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&priv.PublicKey)}))
	pub8 = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubder}))
	return
}

func TestParseKeysPKCS1AndPKCS8(t *testing.T) {
	priv, p1, p8, pub1, pub8 := testPEMs(t)
	for _, p := range []string{p1, p8} {
		k, err := ParsePrivateKey(p)
		if err != nil || k.N.Cmp(priv.N) != 0 {
			t.Errorf("private parse: %v", err)
		}
	}
	for _, p := range []string{pub1, pub8} {
		k, err := ParsePublicKey(p)
		if err != nil || k.N.Cmp(priv.N) != 0 {
			t.Errorf("public parse: %v", err)
		}
	}
	if _, err := ParsePrivateKey("garbage"); err == nil {
		t.Error("garbage should fail")
	}
	if _, err := ParsePrivateKey(pub8); err == nil {
		t.Error("public pem as private should fail")
	}
	if _, err := ParsePublicKey("garbage"); err == nil {
		t.Error("garbage public should fail")
	}
	pub := &priv.PublicKey
	fp := Fingerprint(pub)
	if len(fp) != 64 || fp != strings.ToLower(fp) {
		t.Errorf("bad fingerprint %q", fp)
	}
	back, err := ParsePublicKey(PublicKeyPEM(pub))
	if err != nil || Fingerprint(back) != fp {
		t.Errorf("fingerprint mismatch: %v", err)
	}
}

func TestParseKeysToleratesCRLFAndWhitespace(t *testing.T) {
	_, _, p8, _, pub8 := testPEMs(t)
	wrap := func(s string) string { return "  \n" + strings.ReplaceAll(s, "\n", "\r\n") + "  \n" }
	if _, err := ParsePrivateKey(wrap(p8)); err != nil {
		t.Errorf("private: %v", err)
	}
	if _, err := ParsePublicKey(wrap(pub8)); err != nil {
		t.Errorf("public: %v", err)
	}
}
