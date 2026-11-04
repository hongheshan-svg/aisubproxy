package marswallex

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"
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

// 平台后台给公钥的格式不一：纯 Base64、标题与内容不符、整段一行、从 JSON 里复制的字面 \n、证书。都要认得出，私钥要拒绝。
func TestParsePublicKeyLenientFormats(t *testing.T) {
	priv, p1, p8, pub1, pub8 := testPEMs(t)
	pkix, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pkcs1 := x509.MarshalPKCS1PublicKey(&priv.PublicKey)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix509Name("mw"), NotBefore: time.Unix(0, 0), NotAfter: time.Unix(4102444800, 0)}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString
	body := func(s string) string { // PEM 去掉首尾行后的正文
		lines := strings.Split(strings.TrimSpace(s), "\n")
		return strings.Join(lines[1:len(lines)-1], "\n")
	}
	ok := map[string]string{
		"纯 Base64 PKIX 一行":       b64(pkix),
		"纯 Base64 PKIX 带换行":      body(pub8),
		"纯 Base64 PKCS#1":         b64(pkcs1),
		"带引号的纯 Base64":           `"` + b64(pkix) + `"`,
		"标题写 RSA 内容是 PKIX":       strings.Replace(strings.Replace(pub8, "BEGIN PUBLIC", "BEGIN RSA PUBLIC", 1), "END PUBLIC", "END RSA PUBLIC", 1),
		"标题写 PUBLIC 内容是 PKCS#1":   strings.Replace(strings.Replace(pub1, "BEGIN RSA PUBLIC", "BEGIN PUBLIC", 1), "END RSA PUBLIC", "END PUBLIC", 1),
		"整段一行":                    "-----BEGIN PUBLIC KEY-----" + b64(pkix) + "-----END PUBLIC KEY-----",
		"字面 \\n":                  strings.ReplaceAll(strings.TrimSpace(pub8), "\n", `\n`),
		"正文里夹空格":                  strings.Replace(pub8, "\n", "\n  ", 3),
		"证书 PEM":                  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})),
		"纯 Base64 证书":             b64(certDER),
	}
	for name, in := range ok {
		k, err := ParsePublicKey(in)
		if err != nil || k.N.Cmp(priv.N) != 0 {
			t.Errorf("%s：应能解析，得到 %v", name, err)
		}
	}
	bad := map[string]string{
		"空":            "  ",
		"乱码":           "garbage!!",
		"私钥 PKCS#8 PEM": p8,
		"私钥 PKCS#1 PEM": p1,
		"纯 Base64 私钥":   body(p8),
	}
	for name, in := range bad {
		if _, err := ParsePublicKey(in); err == nil {
			t.Errorf("%s：应拒绝", name)
		}
	}
}

func pkix509Name(cn string) pkix.Name { return pkix.Name{CommonName: cn} }

// 生成的私钥是 2048 位 RSA、PKCS#8 PEM，能被 ParsePrivateKey 读出；每次生成都不同。
func TestGeneratePrivateKeyPEM(t *testing.T) {
	a, err := GeneratePrivateKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a, "-----BEGIN PRIVATE KEY-----") {
		t.Fatalf("应为 PKCS#8 PEM，得到 %.40q", a)
	}
	k, err := ParsePrivateKey(a)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if k.N.BitLen() != 2048 {
		t.Fatalf("位数 = %d，期望 2048", k.N.BitLen())
	}
	b, err := GeneratePrivateKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("两次生成的私钥相同")
	}
}
