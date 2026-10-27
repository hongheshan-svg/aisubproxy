# MarsWallex 接入层 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 新增 MarsWallex 客户端（签名验签、结果分类、27 个接口）、加密密钥存储、Webhook 接收和后台对接页，让后续子项目可以安全调用 MarsWallex。

**Architecture:** 三个新单元互相隔离：`internal/secretbox`（AES-256-GCM）、`internal/marswallex`（纯客户端，不碰数据库）、`internal/db/marswallex.go`（配置与 Webhook 表）；`internal/handler` 把它们接到 Gin 路由，前端新增 `/ops/marswallex` 页。现有 ZovoCard 代码一行不改。

**Tech Stack:** Go 1.24（module `github.com/tuzi/cdk-recharge-system`）、Gin、SQLite（`mattn/go-sqlite3`）、标准库 `crypto/rsa` `crypto/aes`；前端 Vue 3 + Element Plus + Vite。

**Spec:** `docs/superpowers/specs/2026-10-27-marswallex-access-layer-design.md`；接口字段以 `docs/marswallex-openapi-zh.md` 为准。

## Global Constraints

- 只新增代码；`internal/cardplatform`、ZovoCard 的 handler 和路由不改动。
- `internal/marswallex` 只依赖标准库，不 import `internal/db`。
- 金额全程不用浮点数；内部为 int64 分，接口为最多两位小数的字符串。
- 密钥（apiToken、私钥）不写日志、不进审计内容、不在任何接口返回明文。
- 主密钥环境变量名 `APP_MASTER_KEY`（base64 编码的 32 字节），**无默认值**。
- 生产地址预设 `https://api.feibeiglobal.com/portal/api`；测试地址预设 `https://api.feibeiglobal-test.com/portal/api`（标注「未确认」）。
- 客户端从不自动重试；单次请求超时 30 秒。
- Webhook 时间窗口 5 分钟；请求体上限 1MB；响应体读取上限 4MB；上传文件上限 7MB。
- 已知原有失败测试 `TestIssueProductKeepsGPTDefault`、`TestXIssueInfersProductWithoutQueryParameter`（`internal/handler`）不修复，验证时排除说明。
- 所有提交用仓库已配置的 zovocard 身份，提交信息末尾加 `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`。

## Review Focus

1. **地址带尾部斜杠或多写了 `/v1`**（如 `https://…/portal/api/`、`https://…/portal/api/v1`）：应规范化为 `…/portal/api`，签名 URI 仍是 `/portal/api/v1/...`，否则签名必然被拒。→ Task 4 `TestNewClientNormalizesBaseURL`。
2. **粘贴的 PEM 带 Windows 换行、首尾空白**：应能正常解析。→ Task 2 `TestParseKeysToleratesCRLFAndWhitespace`。
3. **查询参数值含空格、`&`、中文**：发送的查询串与签名里的二次编码必须基于同一个原始串，否则签名被拒。→ Task 2 `TestSigningPayloadEncodesSpecialQueryValues`。
4. **Webhook 时间戳是毫秒，或比本机时间快几分钟**：毫秒在窗口内应通过；超前超过 5 分钟应拒绝（按绝对差判断）。→ Task 7 `TestParseWebhookTimestampWindow`。
5. **同一 Webhook 并发重复推送**：只入库一次，两次都返回 200，不报错。→ Task 8 `TestInsertWebhookEventDuplicateIsNotError`。

---

## File Structure

| 文件 | 职责 |
|---|---|
| `backend/internal/secretbox/secretbox.go` | 主密钥加载、加密、解密 |
| `backend/internal/marswallex/sign.go` | 签名原文、RSA 签名验签、`F-Signature` 头、PEM 解析、指纹 |
| `backend/internal/marswallex/types.go` | 金额换算、`Timestamp`、`Count`、分页与响应信封 |
| `backend/internal/marswallex/client.go` | `Config`、`Client`、传输、结果分类、`Error` |
| `backend/internal/marswallex/accounts.go` `cards.go` | 账户 5 个、卡管理 10 个方法及结构体 |
| `backend/internal/marswallex/cardholders.go` `transactions.go` `funds.go` `files.go` | 持卡人 4、卡交易 2、收款 1 + 兑换 2 + 转账 2、上传 1 |
| `backend/internal/marswallex/webhook.go` | Webhook 验签、解析、topic 常量、各事件数据结构 |
| `backend/internal/marswallex/mock_test.go` | 测试用模拟 MarsWallex 服务（Task 4 起共用） |
| `backend/internal/db/marswallex.go` | 设置键常量、加密设置读写、Webhook 表迁移与读写 |
| `backend/internal/handler/marswallex.go` | 后台配置、测试连接、Webhook 列表；从设置构造客户端 |
| `backend/internal/handler/marswallex_webhook.go` | 公开 Webhook 接收 |
| `backend/internal/server/http.go` | 注册路由 |
| `frontend/src/views/admin/MarsWallexIntegration.vue` | 后台对接页 |
| `frontend/src/router/index.ts` `layouts/AdminLayout.vue` `element.ts` | 路由、导航、图标注册 |
| `deploy/docker-deploy.sh` `deploy/app.env.example` | 生成并说明 `APP_MASTER_KEY` |

---

### Task 1: secretbox 加密包与主密钥部署

**Files:**
- Create: `backend/internal/secretbox/secretbox.go`
- Test: `backend/internal/secretbox/secretbox_test.go`
- Modify: `deploy/docker-deploy.sh`（第 72–99 行附近，紧跟 JWT 生成逻辑）
- Modify: `deploy/app.env.example`

**Interfaces:**
- Produces:
  - `var ErrNoMasterKey, ErrBadMasterKey, ErrDecrypt error`
  - `func LoadKey() ([]byte, error)` — 读 `APP_MASTER_KEY`，空 → `ErrNoMasterKey`；非法 base64 或解码后不是 32 字节 → `ErrBadMasterKey`
  - `func Seal(key []byte, name, plaintext string) (string, error)` — 返回 `"v1:" + base64.StdEncoding(nonce12 ‖ ciphertext)`，AAD = `name`
  - `func Open(key []byte, name, sealed string) (string, error)` — 任何不符（前缀、base64、长度、认证失败）一律返回 `ErrDecrypt`

- [ ] **Step 1: 写失败测试**

```go
func testKey(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func TestSealOpenRoundTrip(t *testing.T) {
	s, err := Seal(testKey(1), "marswallex_api_token", "tok-123")
	// err == nil; strings.HasPrefix(s, "v1:"); s 不包含 "tok-123"
	got, err := Open(testKey(1), "marswallex_api_token", s)
	// err == nil; got == "tok-123"
	s2, _ := Seal(testKey(1), "marswallex_api_token", "tok-123")
	// s2 != s（随机数不同）
}

func TestOpenRejectsTamperWrongNameWrongKey(t *testing.T) {
	s, _ := Seal(testKey(1), "k", "secret")
	// Open(testKey(2), "k", s)            → errors.Is(err, ErrDecrypt)
	// Open(testKey(1), "other", s)        → ErrDecrypt
	// 篡改 base64 解码后任一字节再编码     → ErrDecrypt
	// Open(testKey(1), "k", "v2:"+s[3:])  → ErrDecrypt
	// Open(testKey(1), "k", "plain")      → ErrDecrypt
}

func TestLoadKey(t *testing.T) {
	t.Setenv("APP_MASTER_KEY", "")                      // → ErrNoMasterKey
	t.Setenv("APP_MASTER_KEY", "not-base64!!")          // → ErrBadMasterKey
	t.Setenv("APP_MASTER_KEY", base64.StdEncoding.EncodeToString(make([]byte, 16))) // → ErrBadMasterKey
	t.Setenv("APP_MASTER_KEY", base64.StdEncoding.EncodeToString(testKey(7)))       // → 返回 32 字节，等于 testKey(7)
}
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/secretbox/`
Expected: 编译失败（`Seal` / `Open` / `LoadKey` 未定义）

- [ ] **Step 3: 实现 `secretbox.go`**：`aes.NewCipher` + `cipher.NewGCM`，随机数用 `crypto/rand`；`LoadKey` 先 `strings.TrimSpace`。

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/secretbox/ -v`
Expected: PASS，3 个测试

- [ ] **Step 5: 部署脚本生成主密钥**

在 `deploy/docker-deploy.sh` 的 JWT Secret 段之后，照同样写法新增：文件 `$INSTALL_DIR/.master_key`，不存在时 `openssl rand -base64 32` 生成并 `chmod 600`；新建 `.env` 时写入 `APP_MASTER_KEY=…`；已有 `.env` 缺 `APP_MASTER_KEY=` 行时追加。`deploy/app.env.example` 在 `JWT_SECRET` 后增加：

```
# MarsWallex 等密钥的加密主密钥：base64 编码的 32 字节（openssl rand -base64 32）。
# 必填才能在后台保存 MarsWallex 凭证。丢失后已保存的密钥无法解密，只能在后台重新录入。
APP_MASTER_KEY=
```

Run: `bash -n deploy/docker-deploy.sh`
Expected: 无输出（语法正确）

- [ ] **Step 6: 提交**

```bash
git add backend/internal/secretbox deploy/docker-deploy.sh deploy/app.env.example
git commit -m "feat(secretbox): AES-GCM 加密存储与 APP_MASTER_KEY 部署"
```

---

### Task 2: 签名、验签与密钥解析

**Files:**
- Create: `backend/internal/marswallex/sign.go`
- Test: `backend/internal/marswallex/sign_test.go`

**Interfaces:**
- Produces:
  - `func SigningPayload(method, uri string, epoch string, body []byte, rawQuery string) string`
  - `func Sign(priv *rsa.PrivateKey, payload string) (string, error)` — `rsa.SignPKCS1v15` + SHA-256，返回 StdEncoding base64
  - `func Verify(pub *rsa.PublicKey, payload, sigB64 string) error`
  - `func FormatSignatureHeader(epoch string, sig string) string` — `"t=" + epoch + ",v=" + sig`
  - `func ParseSignatureHeader(h string) (epoch, sig string, err error)` — 按 `,` 切分，每段按**第一个** `=` 切键值（签名本身含 `=`）；取 `t` 与第一个以 `v` 开头的键；缺任一报错
  - `func EpochTime(epoch string) (time.Time, error)` — 纯数字；≥13 位按毫秒，否则按秒
  - `func ParsePrivateKey(pemText string) (*rsa.PrivateKey, error)` — 支持 `PRIVATE KEY`（PKCS#8）与 `RSA PRIVATE KEY`（PKCS#1）
  - `func ParsePublicKey(pemText string) (*rsa.PublicKey, error)` — 支持 `PUBLIC KEY`（PKIX）与 `RSA PUBLIC KEY`（PKCS#1）
  - `func PublicKeyPEM(pub *rsa.PublicKey) string` — PKIX PEM
  - `func Fingerprint(pub *rsa.PublicKey) string` — `hex(sha256(PKIX DER))`，小写

签名原文算法（规范 §请求签名）：

```go
// rawQuery 是实际发送的查询串（如 "page_no=1&page_size=10"），签名时整串再 QueryEscape 一次。
func SigningPayload(method, uri, epoch string, body []byte, rawQuery string) string {
	p := method + "&" + uri + "&" + epoch + "&" + string(body)
	if rawQuery != "" {
		p += "&" + url.QueryEscape(rawQuery)
	}
	return p
}
```

- [ ] **Step 1: 写失败测试**（期望值逐字取自官方文档示例）

```go
func TestSigningPayloadMatchesDocExamples(t *testing.T) {
	cases := []struct{ method, uri string; body []byte; q, want string }{
		{"GET", "/portal/api/v1/accounts", nil, "", "GET&/portal/api/v1/accounts&1766123535&"},
		{"POST", "/portal/api/v1/cardholder", []byte(`{"out_card_holder_id":"CH0001","first_name":"Tom"}`), "",
			`POST&/portal/api/v1/cardholder&1766123535&{"out_card_holder_id":"CH0001","first_name":"Tom"}`},
		{"POST", "/portal/api/v1/file/upload", nil, "biz_type=CARDHOLDER_ID_PHOTO",
			"POST&/portal/api/v1/file/upload&1766123535&&biz_type%3DCARDHOLDER_ID_PHOTO"},
		{"GET", "/portal/api/v1/accounts/page", nil, "page_no=1&page_size=10",
			"GET&/portal/api/v1/accounts/page&1766123535&&page_no%3D1%26page_size%3D10"},
	}
	// 每个 case: SigningPayload(c.method, c.uri, "1766123535", c.body, c.q) == c.want
}

func TestSigningPayloadEncodesSpecialQueryValues(t *testing.T) {
	q := url.Values{"k": {"a b&c"}}.Encode() // "k=a+b%26c"
	// SigningPayload("GET", "/x", "1", nil, q) == "GET&/x&1&&k%3Da%2Bb%2526c"
}

func TestSignVerifyRoundTripAndTamper(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	sig, _ := Sign(priv, "GET&/x&1&")
	// Verify(&priv.PublicKey, "GET&/x&1&", sig) == nil
	// Verify(&priv.PublicKey, "GET&/x&2&", sig) != nil
	// Verify(&priv.PublicKey, "GET&/x&1&", "!!") != nil
}

func TestSignatureHeader(t *testing.T) {
	// ParseSignatureHeader("t=1706498637,v=XJk/9+abc=") → ("1706498637", "XJk/9+abc=", nil)
	// ParseSignatureHeader(FormatSignatureHeader("1", "s==")) → ("1", "s==", nil)
	// ParseSignatureHeader("v=abc") → err；ParseSignatureHeader("t=1") → err；ParseSignatureHeader("") → err
}

func TestEpochTime(t *testing.T) {
	// EpochTime("1766123535").Unix() == 1766123535
	// EpochTime("1766123822742").UnixMilli() == 1766123822742
	// EpochTime("12a") → err；EpochTime("") → err
}

func TestParseKeysPKCS1AndPKCS8(t *testing.T) {
	// 用 GenerateKey 生成的私钥分别编码为 PKCS#1、PKCS#8 PEM，ParsePrivateKey 都能解析且 N 相同
	// 公钥分别编码为 PKIX、PKCS#1 PEM，ParsePublicKey 都能解析
	// ParsePrivateKey("garbage") → err；用公钥 PEM 调 ParsePrivateKey → err
	// Fingerprint 长度 64，且 ParsePublicKey(PublicKeyPEM(pub)) 的指纹与 pub 相同
}

func TestParseKeysToleratesCRLFAndWhitespace(t *testing.T) {
	// pemText 换成 "\r\n" 换行并在首尾加 "  \n"，ParsePrivateKey / ParsePublicKey 仍成功
}
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/marswallex/ -run 'Signing|Sign|Signature|Epoch|ParseKeys'`
Expected: 编译失败（函数未定义）

- [ ] **Step 3: 实现 `sign.go`**：PEM 解析前 `strings.TrimSpace` 并把 `\r\n` 换成 `\n`。

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/marswallex/ -v -run 'Signing|Sign|Signature|Epoch|ParseKeys'`
Expected: PASS，7 个测试

- [ ] **Step 5: 提交**

```bash
git add backend/internal/marswallex/sign.go backend/internal/marswallex/sign_test.go
git commit -m "feat(marswallex): 请求/响应签名、F-Signature 解析与 RSA 密钥处理"
```

---

### Task 3: 金额、时间与计数类型

**Files:**
- Create: `backend/internal/marswallex/types.go`
- Test: `backend/internal/marswallex/types_test.go`

**Interfaces:**
- Produces:
  - `func FormatAmount(minor int64) (string, error)` — 负数报错；`12345→"123.45"`、`5→"0.05"`、`100→"1.00"`、`0→"0.00"`
  - `func ParseAmount(s string) (int64, error)` — 只接受 `^\d{1,12}(\.\d{1,2})?$`（规范转账金额格式）
  - `type Timestamp struct{ time.Time }`，`UnmarshalJSON`：JSON 数字或数字字符串（≥1e12 按毫秒，否则秒）、ISO 字符串（`time.RFC3339Nano` 及 `"2006-01-02T15:04:05.000-07:00"`）、`null` → 零值；`MarshalJSON` 输出 Unix 秒数字
  - `type Count int64`，`UnmarshalJSON` 接受 `"12"`、`12`、`null`（→0）
  - `type Page[T any] struct { Count Count `json:"count"`; Data []T `json:"data"` }`
  - `type envelope[T any] struct { Data T `json:"data"` }`（包内使用）

- [ ] **Step 1: 写失败测试**

```go
func TestFormatAmount(t *testing.T) {
	// 12345→"123.45"；5→"0.05"；100→"1.00"；0→"0.00"；-1→err
}

func TestParseAmount(t *testing.T) {
	// "100.50"→10050；"100.5"→10050；"100"→10000；"0.05"→5；"89246.14"→8924614
	// 报错: ""、"-1"、"1.234"、"1e3"、" 1"、"1."、".5"、"abc"、"1234567890123"（13 位整数）
}

func TestTimestampUnmarshal(t *testing.T) {
	// `1766123535` → Unix()==1766123535
	// `1766123822742` → UnixMilli()==1766123822742
	// `"1766123535"` → Unix()==1766123535
	// `"2025-12-15T10:55:03.000+00:00"` → 等于 time.Date(2025,12,15,10,55,3,0,time.UTC)
	// `null` → IsZero()
	// `"bad"` → err
}

func TestCountUnmarshal(t *testing.T) {
	// `"12"`→12；`12`→12；`null`→0；`"x"`→err
}
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/marswallex/ -run 'Amount|Timestamp|Count'`
Expected: 编译失败

- [ ] **Step 3: 实现 `types.go`**：`ParseAmount` 用正则校验后按整数部分与小数部分拼接计算，不经过 `strconv.ParseFloat`。

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/marswallex/ -v -run 'Amount|Timestamp|Count'`
Expected: PASS，4 个测试

- [ ] **Step 5: 提交**

```bash
git add backend/internal/marswallex/types.go backend/internal/marswallex/types_test.go
git commit -m "feat(marswallex): 金额（分）换算与兼容秒/毫秒/ISO 的时间类型"
```

---

### Task 4: 客户端传输与结果分类

**Files:**
- Create: `backend/internal/marswallex/client.go`
- Create: `backend/internal/marswallex/mock_test.go`
- Test: `backend/internal/marswallex/client_test.go`

**Interfaces:**
- Consumes: Task 2 `SigningPayload`、`Sign`、`Verify`、`FormatSignatureHeader`、`ParseSignatureHeader`；Task 3 `envelope`
- Produces:
  - `type Config struct { BaseURL, DeveloperID, APIToken string; PrivateKey *rsa.PrivateKey; PlatformPublicKey *rsa.PublicKey; HTTPClient *http.Client; Now func() time.Time }`
  - `type Outcome int`，常量 `OutcomeSuccess`(1)、`OutcomeRejected`、`OutcomeUnknown`、`OutcomeNotSent`；`String()` 依次为 `"success"`、`"rejected"`、`"unknown"`、`"not_sent"`
  - `type Reason string`，常量 `ReasonConfig="config"`、`ReasonDNS="dns"`、`ReasonDial="dial"`、`ReasonTimeout="timeout"`、`ReasonConnection="connection"`、`ReasonSignatureMissing="signature_missing"`、`ReasonSignatureInvalid="signature_invalid"`、`ReasonStatus="status"`、`ReasonDecode="decode"`
  - `type Error struct { Outcome Outcome; Reason Reason; HTTPStatus int; Code, Message string; Err error }`，实现 `Error()`、`Unwrap()`
  - `func OutcomeOf(err error) Outcome` — `nil`→Success；`*Error`→其 Outcome；其他→Unknown
  - `func NewClient(cfg Config) (*Client, error)` — 缺 BaseURL/DeveloperID/APIToken/任一密钥，或 BaseURL 不是 https（`http` 仅允许 host 为 `127.0.0.1` / `localhost`）→ `&Error{Outcome: OutcomeNotSent, Reason: ReasonConfig}`；BaseURL 去掉尾部 `/` 和尾部 `/v1`；`HTTPClient` 为 nil 时用 `&http.Client{Timeout: 30 * time.Second}`；`Now` 为 nil 时用 `time.Now`
  - 包内：`func (c *Client) call(ctx context.Context, method, path, rawQuery string, body any, out any) error` — JSON 请求（body 为 nil 时不发请求体）；`func (c *Client) send(ctx context.Context, method, path, rawQuery, contentType string, payload []byte, signedBody []byte, out any) error` — 底层，`signedBody` 为参与签名的字节（上传时为 nil）
  - 包内：`func buildQuery(kv ...string) string` — 成对参数按传入顺序拼接，跳过空值，键值用 `url.QueryEscape`

分类规则（规范 §5.3，是本任务的核心算法）：

```go
// 网络错误：只有能证明一个字节没发出去的才算 NotSent
func classifyTransportErr(err error) (Outcome, Reason) {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) { return OutcomeNotSent, ReasonDNS }
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" { return OutcomeNotSent, ReasonDial }
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) { return OutcomeUnknown, ReasonTimeout }
	return OutcomeUnknown, ReasonConnection
}
// 收到响应后：先验签（缺签名→Unknown/SignatureMissing，验不过→Unknown/SignatureInvalid），再看状态：
//   200 → 解析 envelope 到 out，失败→Unknown/Decode
//   400/401/403/404 → Rejected/Status，Code/Message 取自响应 {"code","message"}
//   其他 → Unknown/Status（尽量带上 Code/Message）
// 响应验签原文：epoch + "&" + 原始响应体（epoch 原样，不换算）
```

请求头：`Authorization: Basic base64(DeveloperID:APIToken)`、`F-Signature`（epoch = `strconv.FormatInt(c.now().Unix(), 10)`）、`Accept: application/json`，有 JSON 体时 `Content-Type: application/json`。签名 URI = 解析后 BaseURL 的 path + `path`（如 `/portal/api` + `/v1/accounts/page`）。

`mock_test.go` 提供共用的模拟平台：

```go
type mockPlatform struct {
	t        *testing.T
	ourPub   *rsa.PublicKey  // 用来验请求签名
	platPriv *rsa.PrivateKey // 用来签响应
	Server   *httptest.Server
	Last     *http.Request   // 最近一次请求（Body 已读入 LastBody）
	LastBody []byte
	Reply    func(w http.ResponseWriter, r *http.Request) // 默认返回 200 + `{"data":{}}`
}
func newMockPlatform(t *testing.T) (*mockPlatform, *Client) // 生成两对密钥，起服务，返回 BaseURL=Server.URL+"/portal/api" 的 Client
func (m *mockPlatform) signedJSON(w http.ResponseWriter, status int, body string)   // 秒级 epoch 签名
func (m *mockPlatform) signedJSONMillis(w http.ResponseWriter, status int, body string)
```

模拟服务对每个请求校验：`Authorization` 正确；`F-Signature` 用 `SigningPayload(r.Method, r.URL.Path, t, 请求体, r.URL.RawQuery)` 能被 `ourPub` 验证（上传请求的请求体按空处理）。校验失败直接 `t.Errorf`。

- [ ] **Step 1: 写失败测试**（`client_test.go`，用一个只读调用 `c.call(ctx, "GET", "/v1/ping", "a=1&b=2", nil, &out)` 驱动）

```go
func TestCallSuccessSignedResponse(t *testing.T)          // 200 签名 `{"data":{"x":"1"}}` → err==nil，out.X=="1"；mock 收到 RawQuery=="a=1&b=2"
func TestCallSuccessMillisecondSignature(t *testing.T)    // 200 毫秒签名 → err==nil
func TestCallUnsignedOKIsUnknown(t *testing.T)            // 200 无 F-Signature → Outcome Unknown，Reason SignatureMissing
func TestCallBadSignatureIsUnknown(t *testing.T)          // 200 签名对不上 → Unknown，SignatureInvalid
func TestCallSignedRejected(t *testing.T)                 // 400 签名 `{"code":"PARAM_ERROR","message":"bad"}` → Rejected，HTTPStatus 400，Code "PARAM_ERROR"，Message "bad"
func TestCallUnsignedRejectionIsUnknown(t *testing.T)     // 400 无签名 → Unknown
func TestCallAmbiguousStatusesAreUnknown(t *testing.T)    // 201/202/409/500 签名 → 均 Unknown，Reason Status
func TestCallUndecodableOKIsUnknown(t *testing.T)         // 200 签名 `not json` → Unknown，Decode
func TestCallTimeoutIsUnknown(t *testing.T)               // HTTPClient.Timeout=100ms，服务端 sleep 300ms → Unknown，Timeout
func TestCallConnectionDropIsUnknown(t *testing.T)        // 服务端 Hijack 后直接关闭连接 → Unknown
func TestCallConnectionRefusedIsNotSent(t *testing.T)     // BaseURL 指向已关闭的 httptest 地址 → NotSent，Dial
func TestCallDNSFailureIsNotSent(t *testing.T)            // BaseURL "https://no-such-host.invalid/portal/api" → NotSent，DNS
func TestNewClientRejectsIncompleteConfig(t *testing.T)   // 逐项置空 / http 非本机 → err 且 OutcomeOf(err)==NotSent
func TestNewClientNormalizesBaseURL(t *testing.T)         // BaseURL 用 Server.URL+"/portal/api/" 与 +"/portal/api/v1"，请求到达路径都是 "/portal/api/v1/ping"，签名通过
func TestOutcomeOf(t *testing.T)                          // nil→Success；errors.New("x")→Unknown；包装后的 *Error 能被识别
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/marswallex/ -run 'Call|NewClient|OutcomeOf'`
Expected: 编译失败

- [ ] **Step 3: 实现 `client.go`**：响应体用 `io.LimitReader(resp.Body, 4<<20)` 读取；不设置 `Accept-Encoding`（交给 Go 透明解压）。

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/marswallex/ -v -run 'Call|NewClient|OutcomeOf'`
Expected: PASS，15 个测试

- [ ] **Step 5: 提交**

```bash
git add backend/internal/marswallex/client.go backend/internal/marswallex/mock_test.go backend/internal/marswallex/client_test.go
git commit -m "feat(marswallex): 签名传输与成功/被拒/未知/未发出结果分类"
```

---

### Task 5: 账户与卡管理接口（15 个方法）

**Files:**
- Create: `backend/internal/marswallex/accounts.go`、`backend/internal/marswallex/cards.go`
- Test: `backend/internal/marswallex/accounts_cards_test.go`

**Interfaces:**
- Consumes: Task 4 `call`、`buildQuery`、mock；Task 3 `Page`、`Timestamp`、`Count`
- Produces（结构体字段按 `docs/marswallex-openapi-zh.md` 对应章节，JSON 名照抄；金额字段一律 `string`；时间字段一律 `Timestamp`）：

| 方法 | HTTP | 查询参数顺序 / 请求体 | 返回 |
|---|---|---|---|
| `CreateAccount(ctx, CreateAccountRequest{AccountName, OutAccountNo string; Purpose *string})` | `POST /v1/accounts` | 体为 `{"data":{...}}`（规范如此，代码注释说明） | `*SubAccount` |
| `ListAccounts(ctx, ListAccountsQuery{PageNo, PageSize int; AccountType, AccountNo string})` | `GET /v1/accounts/page` | `page_no,page_size,account_type,account_no` | `*Page[Account]` |
| `ListAccountSerials(ctx, AccountSerialsQuery{PageNo, PageSize int; AccountNo, Currency string; StartTime, EndTime int64})` | `GET /v1/accounts/serials` | `page_no,page_size,account_no,currency,start_time,end_time` | `*Page[AccountSerial]` |
| `GetAccount(ctx, accountNo string)` | `GET /v1/accounts/detail/{account_no}` | 路径段 `url.PathEscape` | `*Account` |
| `UpdateAccountStatus(ctx, accountNo string, UpdateAccountStatusRequest{AccountStatus, Remark string})` | `PUT /v1/accounts/{account_no}/status` | JSON 体 | `*AccountRef{AccountNo, OutAccountNo}` |
| `ListCardProducts(ctx)` | `GET /v1/card/product` | — | `[]CardProduct` |
| `ApplyCard(ctx, ApplyCardRequest{OutOrderNo, ProductID, Amount, Currency, CardHolderID, CardName, ExpiryDate string; AccountNo, Remark, FeeDeductType string 均 omitempty; Limit *CardLimit})` | `POST /v2/card` | JSON 体 | `*ApplyCardResult{OutOrderNo, TransactionID}` |
| `GetCardApplyOrder(ctx, OrderRef{OutOrderNo, TransactionID string})` | `GET /v1/card/apply-order` | `out_order_no,transaction_id` | `*CardApplyOrder` |
| `RechargeCard(ctx, CardTransferRequest{OutOrderNo, CardID, Currency, Amount string})` | `POST /v1/card/recharge-transaction` | JSON 体 | `*CardBalanceTransaction` |
| `WithdrawCard(ctx, CardTransferRequest)` | `POST /v1/card/withdraw-transaction` | JSON 体 | `*CardBalanceTransaction` |
| `UpdateCardStatus(ctx, UpdateCardStatusRequest{CardID, CardStatus string; Remark string omitempty; AccountNo *int64 omitempty})` | `PUT /v1/card/status` | JSON 体；`account_no` 按规范为整数（注释说明） | `*CardStatusResult{CardID, CardStatus}` |
| `GetCardTransferOrder(ctx, OrderRef)` | `GET /v1/card/transfer-order` | `out_order_no,transaction_id` | `*CardBalanceTransaction` |
| `ListCardBalanceTransactions(ctx, CardBalanceTransactionsQuery{PageNo, PageSize int; CardID string; StartTime, EndTime int64; OutOrderNo string})` | `GET /v1/card/balance-transactions` | `page_no,page_size,card_id,start_time,end_time,out_order_no` | `*Page[CardBalanceTransaction]` |
| `GetCardSensitive(ctx, cardID string)` | `GET /v1/card/sensitive/{card_id}` | — | `*CardSensitive` |
| `GetCard(ctx, cardID string)` | `GET /v1/card/detail/{card_id}` | — | `*CardDetail` |

所有方法签名形如 `func (c *Client) X(ctx context.Context, …) (T, error)`。int 查询参数为 0、string 为空时不发送。

- [ ] **Step 1: 写失败测试**——表驱动 `TestAccountAndCardRequests`：每行调用一个方法，断言 mock 收到的 `Method`、`URL.Path`（含 `/portal/api` 前缀）、`URL.RawQuery`、请求体 JSON；再用 mock 返回规范样例数据，断言解析出的关键字段。必须包含的断言：

```go
// CreateAccount → body == `{"data":{"account_name":"SubA","out_account_no":"SUB1"}}`
// ListAccounts{PageNo:1,PageSize:10} → RawQuery == "page_no=1&page_size=10"
// ListAccounts 响应 `{"count":"2","data":[{"account_no":"2000519969952288769","create_time":1766123535,
//     "balance_list":[{"currency":"USD","available_balance":"89246.14","freeze_balance":"0.00"}]}]}`
//   → Count==2，Data[0].AccountNo=="2000519969952288769"，Data[0].BalanceList[0].AvailableBalance=="89246.14"
// GetAccount("a/b") → Path == "/portal/api/v1/accounts/detail/a%2Fb"（r.URL.RawPath 或 EscapedPath）
// UpdateCardStatus{CardID:"c1",CardStatus:"CANCELED",AccountNo:ptr(int64(2000519969952288769))}
//   → body 中 account_no 为 JSON 数字 2000519969952288769（不是字符串）
// UpdateCardStatus 不传 AccountNo → body 不含 account_no
// ApplyCard 只填必填字段 → body 不含 account_no/remark/fee_deduct_type/limit
// GetCardApplyOrder{TransactionID:"T1"} → RawQuery == "transaction_id=T1"
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/marswallex/ -run TestAccountAndCardRequests`
Expected: 编译失败

- [ ] **Step 3: 实现 `accounts.go`、`cards.go`**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/marswallex/ -v -run TestAccountAndCardRequests`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/marswallex/accounts.go backend/internal/marswallex/cards.go backend/internal/marswallex/accounts_cards_test.go
git commit -m "feat(marswallex): 账户与卡管理接口"
```

---

### Task 6: 持卡人、卡交易、资金与上传接口（12 个方法）

**Files:**
- Create: `backend/internal/marswallex/cardholders.go`、`transactions.go`、`funds.go`、`files.go`
- Test: `backend/internal/marswallex/other_endpoints_test.go`

**Interfaces:**
- Consumes: 同 Task 5；Task 4 `send`
- Produces：

| 方法 | HTTP | 查询参数顺序 / 请求体 | 返回 |
|---|---|---|---|
| `CreateCardholder(ctx, CreateCardholderRequest{OutCardHolderID string; CardholderProfile})` | `POST /v1/cardholder` | JSON 体（Profile 内嵌展开） | `*CardholderRef{CardHolderID}` |
| `GetCardholder(ctx, cardHolderID string)` | `GET /v1/cardholder/{card_holder_id}` | — | `*CardholderDetail` |
| `UpdateCardholder(ctx, cardHolderID string, CardholderProfile)` | `PUT /v1/cardholder/{card_holder_id}` | JSON 体（不含 out_card_holder_id） | `*CardholderRef` |
| `UpdateCardholderStatus(ctx, cardHolderID, status string)` | `PUT /v1/cardholder/status/{card_holder_id}` | `{"status":…}` | `*CardholderStatusResult{CardHolderID, OutCardHolderID, Status}` |
| `ListAuthTransactions(ctx, CardTransactionsQuery{PageNo, PageSize int; CardID, TransactionID string; StartTime, EndTime int64})` | `GET /v1/card/transactions/authorization` | `page_no,page_size,card_id,transaction_id,start_time,end_time` | `*Page[AuthTransaction]` |
| `ListSettlementTransactions(ctx, CardTransactionsQuery)` | `GET /v1/card/transactions/settlement` | 同上 | `*Page[SettlementTransaction]` |
| `ListReceivingAddresses(ctx)` | `GET /v1/receiving/receiving-address` | — | `[]ReceivingAddress` |
| `CreateConversion(ctx, ConversionRequest{OutOrderNo, SellCurrency, BuyCurrency string; SellAmount string omitempty})` | `POST /v1/conversion` | JSON 体 | `*ConversionResult{TransactionID}` |
| `GetConversion(ctx, outOrderNo string)` | `GET /v1/conversion/{out_order_no}` | — | `*ConversionDetail` |
| `CreateTransfer(ctx, TransferRequest{OutOrderNo, PayerAccountNo, PayeeAccountNo, Amount, Currency string; Remark string omitempty})` | `POST /v1/transfer` | JSON 体 | `*TransferResult{OutOrderNo, OrderNo, Status}` |
| `GetTransfer(ctx, TransferRef{OutOrderNo, OrderNo string})` | `GET /v1/transfer/order` | `out_order_no,order_no` | `*TransferDetail` |
| `UploadFile(ctx, bizType, filename string, content []byte)` | `POST /v1/file/upload?biz_type=…` | multipart，只含 `file` 字段；签名时请求体按空处理 | `*UploadResult{FileID}` |

`CardholderProfile` 字段为规范「添加持卡人」请求体去掉 `out_card_holder_id`；`Address{Country, State, City, Address, PostCode}`；`IDPhotoFileIDs []string`；`SSN string omitempty`；中文姓名 omitempty。`UploadFile`：`len(content) > 7<<20` 时直接返回 `&Error{Outcome: OutcomeNotSent, Reason: ReasonConfig, Message: "文件超过 7MB"}`，不发请求。

- [ ] **Step 1: 写失败测试**——表驱动 `TestOtherEndpointRequests`，同 Task 5 的断言方式。必须包含：

```go
// UpdateCardholder 的 body 不含 "out_card_holder_id"
// UpdateCardholderStatus("CH1","DISABLED") → PUT /portal/api/v1/cardholder/status/CH1，body == `{"status":"DISABLED"}`
// ListAuthTransactions{PageNo:1,PageSize:20,CardID:"c1",StartTime:1,EndTime:2}
//   → RawQuery == "page_no=1&page_size=20&card_id=c1&start_time=1&end_time=2"
// GetTransfer 响应 create_time 为毫秒 1766123822742 → CreateTime.UnixMilli()==1766123822742
// CreateTransfer 响应 `{"count":null,"data":{"out_order_no":"M1","order_no":"P1","status":"PROCESSING"}}` → Status=="PROCESSING"
func TestUploadFileMultipartAndSigning(t *testing.T) {
	// RawQuery == "biz_type=CARDHOLDER_ID_PHOTO"；Content-Type 以 "multipart/form-data; boundary=" 开头
	// multipart 只有一个 part，FormName()=="file"，内容等于上传字节
	// mock 按「请求体为空」验签通过（mock 对 /v1/file/upload 用空体计算签名原文）
}
func TestUploadFileTooLargeIsNotSent(t *testing.T) {
	// content 7<<20+1 字节 → OutcomeOf(err)==NotSent，且 mock 未收到请求
}
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/marswallex/ -run 'TestOtherEndpointRequests|TestUploadFile'`
Expected: 编译失败

- [ ] **Step 3: 实现四个文件**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/marswallex/ -v -run 'TestOtherEndpointRequests|TestUploadFile'`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/marswallex/cardholders.go backend/internal/marswallex/transactions.go backend/internal/marswallex/funds.go backend/internal/marswallex/files.go backend/internal/marswallex/other_endpoints_test.go
git commit -m "feat(marswallex): 持卡人、卡交易、收款兑换转账与文件上传接口"
```

---

### Task 7: Webhook 验签与解析

**Files:**
- Create: `backend/internal/marswallex/webhook.go`
- Test: `backend/internal/marswallex/webhook_test.go`

**Interfaces:**
- Consumes: Task 2 `ParseSignatureHeader`、`Verify`、`EpochTime`；Task 3 `Timestamp`
- Produces:
  - topic 常量：`TopicCardApplyResult="CARD_APPLY_RESULT"`、`TopicCardStatusChanged="CARD_STATUS_CHANGED"`、`TopicCardholderStatusChange="CARDHOLDER_STATUS_CHANGE"`、`TopicAccountStatusChanged="ACCOUNT_STATUS_CHANGED"`、`TopicAuthEvent="AUTH_EVENT"`、`TopicSettleEvent="SETTLE_EVENT"`、`TopicCardTransferResult="CARD_TRANSFER_RESULT"`、`TopicConversionResult="CONVERSION_RESULT"`、`TopicTransferResult="TRANSFER_RESULT"`、`TopicCard3DSOTP="CARD_3DS_OTP"`
  - `var ErrWebhookSignature, ErrWebhookStale, ErrWebhookPayload error`
  - `const WebhookWindow = 5 * time.Minute`
  - `type WebhookEvent struct { Topic, MessageID string; Data json.RawMessage; Raw []byte }`
  - `func ParseWebhook(signatureHeader string, raw []byte, platformPub *rsa.PublicKey, now time.Time) (*WebhookEvent, error)` — 依次：解析头（失败→`ErrWebhookSignature`）、验 `epoch+"&"+raw`（失败→`ErrWebhookSignature`）、`|now−EpochTime(epoch)| > WebhookWindow`→`ErrWebhookStale`、解析 JSON（失败→`ErrWebhookPayload`）；消息 ID 取 `message_id`，空则 `messageId`，再空则 `hex(sha256(raw))`；`data` 若是 JSON 字符串则先解析出其中的 JSON
  - `func (e *WebhookEvent) DecodeData(v any) error`
  - 事件数据结构体（字段按规范各 Webhook 章节）：`CardApplyResultData`、`CardStatusChangedData`、`CardholderStatusChangeData`、`AccountStatusChangedData`、`AuthTransactionWebhook`、`SettlementTransactionWebhook`、`CardTransferResultData`（同 `CardBalanceTransaction`，可用类型别名）、`ConversionResultData`（同 `ConversionDetail`）、`TransferResultData`（同 `TransferDetail`）、`Card3DSOTPData`

- [ ] **Step 1: 写失败测试**（用测试内生成的平台私钥签名）

```go
func TestParseWebhookValid(t *testing.T) {
	// raw = `{"topic":"CARD_APPLY_RESULT","message_id":"m1","data":{"card_id":"C1","status":"SUCCESS"}}`
	// → Topic=="CARD_APPLY_RESULT"，MessageID=="m1"；DecodeData 到 CardApplyResultData 得 CardID=="C1"
}
func TestParseWebhookLegacyShape(t *testing.T) {
	// raw = `{"topic":"CARD_APPLY_RESULT","messageId":"m2","data":"{\"card_id\":\"C2\"}"}`
	// → MessageID=="m2"；DecodeData 得 CardID=="C2"
}
func TestParseWebhookMissingMessageIDUsesHash(t *testing.T) {
	// 无消息 ID → MessageID == hex(sha256(raw))，长度 64
}
func TestParseWebhookRejectsBadSignature(t *testing.T) {
	// 头缺失 / 签名错 / 用别的私钥签 / raw 被改一个字节 → errors.Is(err, ErrWebhookSignature)
}
func TestParseWebhookTimestampWindow(t *testing.T) {
	// now 固定；t = now−4m（秒）→ 通过；t = now−4m（毫秒）→ 通过
	// t = now−6m → ErrWebhookStale；t = now+6m → ErrWebhookStale
}
func TestParseWebhookBadJSON(t *testing.T) {
	// 签名正确但 raw = `not json` → ErrWebhookPayload
}
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/marswallex/ -run ParseWebhook`
Expected: 编译失败

- [ ] **Step 3: 实现 `webhook.go`**

- [ ] **Step 4: 运行确认通过，并跑整个包**

Run: `cd backend && go test ./internal/marswallex/ -v && go vet ./internal/marswallex/`
Expected: 全部 PASS，vet 无输出

- [ ] **Step 5: 提交**

```bash
git add backend/internal/marswallex/webhook.go backend/internal/marswallex/webhook_test.go
git commit -m "feat(marswallex): Webhook 验签、时间窗口与新旧两种消息格式解析"
```

---

### Task 8: 数据库：加密设置与 Webhook 表

**Files:**
- Create: `backend/internal/db/marswallex.go`
- Test: `backend/internal/db/marswallex_test.go`
- Modify: `backend/internal/db/db.go`（`createTables` 末尾 `return` 前调用 `MigrateMarsWallex()`）

**Interfaces:**
- Consumes: Task 1 `secretbox.LoadKey`、`Seal`、`Open`
- Produces:
  - 设置键常量：`SettingMarsWallexBaseURL="marswallex_base_url"`、`SettingMarsWallexDeveloperID="marswallex_developer_id"`、`SettingMarsWallexAPIToken="marswallex_api_token"`、`SettingMarsWallexPrivateKey="marswallex_private_key"`、`SettingMarsWallexPlatformPublicKey="marswallex_platform_public_key"`
  - `func SetSecretSetting(key, plaintext string) error` — `LoadKey` 失败原样返回该错误；`Seal(key名作 AAD)` 后 `SetSetting`
  - `func GetSecretSetting(key string) (string, error)` — 未设置返回 `"", nil`；已设置则 `LoadKey` + `Open`
  - `func MigrateMarsWallex() error` — 建表：

```sql
CREATE TABLE IF NOT EXISTS marswallex_webhook_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	message_id TEXT NOT NULL UNIQUE,
	topic TEXT NOT NULL DEFAULT '',
	payload TEXT NOT NULL,
	received_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	processed_at DATETIME,
	process_error TEXT
);
CREATE INDEX IF NOT EXISTS idx_mw_webhook_received ON marswallex_webhook_events(received_at);
```

  - `type MarsWallexWebhookEvent struct { ID int64; MessageID, Topic, Payload string; ReceivedAt time.Time; ProcessedAt *time.Time; ProcessError string }`
  - `func InsertMarsWallexWebhookEvent(messageID, topic, payload string) (inserted bool, err error)` — `INSERT OR IGNORE`，用 `RowsAffected` 判断
  - `func ListMarsWallexWebhookEvents(limit int) ([]MarsWallexWebhookEvent, error)` — `id DESC`；`limit<=0` 用 50，上限 200

- [ ] **Step 1: 写失败测试**（`sql.Open("sqlite3", ":memory:")`，`SetMaxOpenConns(1)`，替换 `DB` 并在 `t.Cleanup` 还原；手动建 `site_settings` 表后调用 `MigrateMarsWallex()`）

```go
func TestSecretSettingRoundTripAndStoredEncrypted(t *testing.T) {
	// t.Setenv("APP_MASTER_KEY", base64(32 字节))
	// SetSecretSetting(SettingMarsWallexAPIToken, "tok") == nil
	// GetSetting 读出的原始值以 "v1:" 开头且不含 "tok"
	// GetSecretSetting 返回 "tok"
	// 未设置的键 GetSecretSetting → "", nil
}
func TestSecretSettingWithoutMasterKey(t *testing.T) {
	// APP_MASTER_KEY="" → SetSecretSetting 返回 errors.Is(err, secretbox.ErrNoMasterKey)，且数据库无此键
}
func TestSecretSettingWrongMasterKey(t *testing.T) {
	// 用 key A 保存，换 key B 读取 → errors.Is(err, secretbox.ErrDecrypt)
}
func TestInsertWebhookEventDuplicateIsNotError(t *testing.T) {
	// 第一次 (true, nil)；同 message_id 第二次 (false, nil)；表内 1 行
}
func TestListWebhookEventsOrderAndLimit(t *testing.T) {
	// 插 3 条 → List(2) 返回 2 条，按 id 倒序；ProcessedAt 为 nil
}
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/db/ -run 'SecretSetting|WebhookEvent'`
Expected: 编译失败

- [ ] **Step 3: 实现 `marswallex.go` 并在 `createTables` 调用 `MigrateMarsWallex()`**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/db/ -v -run 'SecretSetting|WebhookEvent'`
Expected: PASS，5 个测试

- [ ] **Step 5: 提交**

```bash
git add backend/internal/db/marswallex.go backend/internal/db/marswallex_test.go backend/internal/db/db.go
git commit -m "feat(db): MarsWallex 加密设置与 Webhook 事件表"
```

---

### Task 9: 后台接口、Webhook 接收与路由

**Files:**
- Create: `backend/internal/handler/marswallex.go`、`backend/internal/handler/marswallex_webhook.go`
- Test: `backend/internal/handler/marswallex_test.go`
- Modify: `backend/internal/server/http.go`（Webhook 路由放在 `api.POST("/webhooks/cardplatform", …)` 之后；后台路由放在 `admin` 组内）

**Interfaces:**
- Consumes: Task 2 `ParsePrivateKey`、`ParsePublicKey`、`PublicKeyPEM`、`Fingerprint`；Task 4 `NewClient`、`OutcomeOf`、`*Error`；Task 5 `ListAccounts`；Task 7 `ParseWebhook`、`ErrWebhookSignature`、`ErrWebhookStale`；Task 8 全部；现有 `auditAdmin(c, action, detail)`、`db.GetSetting`、`db.SetSetting`
- Produces:
  - `func marsWallexClientFromSettings() (*marswallex.Client, error)` — 读 5 个设置（密钥解密），解析 PEM，`base_url` 为空用生产预设，调用 `NewClient`；任何读取或解析失败返回 `&marswallex.Error{Outcome: marswallex.OutcomeNotSent, Reason: marswallex.ReasonConfig, Message: 中文原因}`
  - 路由与处理函数：

| 路由 | 处理函数 |
|---|---|
| `GET /api/v1/admin/marswallex/config` | `AdminGetMarsWallexConfig` |
| `PUT /api/v1/admin/marswallex/config` | `AdminPutMarsWallexConfig` |
| `POST /api/v1/admin/marswallex/test` | `AdminTestMarsWallex` |
| `GET /api/v1/admin/marswallex/webhook-events` | `AdminListMarsWallexWebhookEvents` |
| `POST /api/v1/webhooks/marswallex` | `MarsWallexWebhook` |

JSON 约定：

```
GET/PUT /config 响应：
{ "base_url", "base_url_presets": [{"id":"prod","label":"生产","url":…},{"id":"test","label":"测试（地址未确认）","url":…}],
  "developer_id", "api_token_set": bool,
  "private_key_set": bool, "public_key_pem", "public_key_fingerprint",
  "platform_public_key_set": bool, "platform_public_key_fingerprint",
  "master_key_ready": bool, "master_key_error",
  "webhook_url" }            // = scheme + "://" + host + "/api/v1/webhooks/marswallex"；scheme 优先 X-Forwarded-Proto，否则看 c.Request.TLS
PUT /config 请求：{ "base_url"?, "developer_id"?, "api_token"?, "private_key"?, "platform_public_key"? }（全为可选字符串指针）
  - api_token / private_key 为空串 → 不修改
  - base_url 须为 https，或 host 为 127.0.0.1/localhost 的 http；否则 400
  - private_key / platform_public_key 解析失败 → 400 {"error":"私钥无法解析：…"} / {"error":"平台公钥无法解析：…"}
  - 主密钥未就绪且要写密钥 → 400 {"error":"服务器未配置 APP_MASTER_KEY，无法保存密钥"}
  - 先全部校验，校验都通过才写库
  - 审计：auditAdmin(c, "update_marswallex_config", "changed: "+逗号分隔的字段名)，不含值
POST /test 响应（HTTP 恒 200）：
{ "outcome": "success|rejected|unknown|not_sent", "reason", "http_status", "code", "message", "hint",
  "accounts": [{ "account_no","account_name","account_type","account_status",
                 "balances":[{"currency","available_balance","freeze_balance"}] }] }
GET /webhook-events?limit= 响应：{ "events": [{ "id","message_id","topic","received_at","processed_at","process_error" }] }
```

测试连接提示（`hint`），按顺序取第一个匹配：

| 条件 | 提示 |
|---|---|
| `not_sent` + `config` | `配置不完整：` + Message |
| `not_sent` + `dns`/`dial` | `无法连接 MarsWallex，请检查服务器网络和接口地址` |
| HTTP 401 或 403 | `认证或签名被拒：请检查 developerId、apiToken，并确认交给 MarsWallex 的公钥与此处私钥是同一对` |
| HTTP 404 | `接口地址不对：请检查地址是否包含 /portal/api 前缀` |
| `signature_missing`/`signature_invalid` | `响应签名验证失败：请检查平台公钥是否正确` |
| `timeout`/`connection` | `请求超时或连接中断，请稍后重试` |
| 其他非成功 | `MarsWallex 返回 HTTP {status}：{message}` |
| 成功 | `连接成功` |

`MarsWallexWebhook`：读体（`io.LimitReader` 1MB+1，超过 1MB → 413）；平台公钥未配置 → 503；`ParseWebhook(header "F-Signature", raw, pub, time.Now())`：签名或过期错误 → 401，载荷错误 → 400；`InsertMarsWallexWebhookEvent` 出错 → 500；否则 200 `{"ok":true}`（重复消息同样 200）。

- [ ] **Step 1: 写失败测试**（`gin.SetMode(gin.TestMode)`；内存库建 `site_settings`、`admin_audit_logs`（照 `db.go` 中定义）并 `MigrateMarsWallex()`；`t.Setenv("APP_MASTER_KEY", …)`；直接调用处理函数，不经过 JWT 中间件）

```go
func TestMarsWallexPutConfigStoresEncryptedAndMasksOnRead(t *testing.T)
	// PUT developer_id/api_token/private_key(PKCS#8)/platform_public_key/base_url=生产预设 → 200
	// 响应 api_token_set==true，且整个响应 JSON 不含 apiToken 原文和私钥 PEM 正文
	// public_key_fingerprint == marswallex.Fingerprint(&priv.PublicKey)
	// site_settings 中 marswallex_api_token 以 "v1:" 开头
func TestMarsWallexPutConfigEmptySecretKeepsOldValue(t *testing.T)
	// 再 PUT {"api_token":"","developer_id":"d2"} → GetSecretSetting 仍是旧 token，developer_id 变为 d2
func TestMarsWallexPutConfigRejectsBadPEMAndBadURL(t *testing.T)
	// private_key:"garbage" → 400 且错误含 "私钥无法解析"；base_url:"http://example.com" → 400；两种情况均未写库
func TestMarsWallexPutConfigWithoutMasterKey(t *testing.T)
	// APP_MASTER_KEY="" 时 PUT api_token → 400 "服务器未配置 APP_MASTER_KEY"；GET 返回 master_key_ready==false
func TestMarsWallexTestConnectionSuccess(t *testing.T)
	// httptest 模拟 MarsWallex（复用签名函数，不能 import 测试包里的 mock）：
	// 校验请求为 GET /portal/api/v1/accounts/page?page_no=1&page_size=10，签名正确，返回签名的账户分页
	// 配置 base_url=Server.URL+"/portal/api" → outcome "success"，hint "连接成功"，accounts[0].balances[0].available_balance=="89246.14"
func TestMarsWallexTestConnectionHints(t *testing.T)
	// 模拟返回签名 401 → hint 含 "认证或签名被拒"；签名 404 → 含 "/portal/api"；无签名 200 → 含 "响应签名验证失败"
	// 未配置任何凭证 → outcome "not_sent"，hint 以 "配置不完整" 开头
func TestMarsWallexWebhookEndpoint(t *testing.T)
	// 平台公钥已配置；签名正确 → 200 且入库；同一请求再发 → 200 且仍 1 行
	// 签名错误 → 401 且未入库；时间戳 10 分钟前 → 401；未配置平台公钥 → 503；体积 > 1MB → 413
	// DROP TABLE marswallex_webhook_events 后发合法请求 → 500（让对方重试）
func TestMarsWallexListWebhookEvents(t *testing.T)
	// 入库 2 条 → GET 返回 events 长度 2，第一条 id 最大，processed_at 为 null
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/handler/ -run TestMarsWallex`
Expected: 编译失败

- [ ] **Step 3: 实现两个 handler 文件并注册路由**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/handler/ -v -run TestMarsWallex && go build ./...`
Expected: PASS，8 个测试；build 无输出

- [ ] **Step 5: 提交**

```bash
git add backend/internal/handler/marswallex.go backend/internal/handler/marswallex_webhook.go backend/internal/handler/marswallex_test.go backend/internal/server/http.go
git commit -m "feat(marswallex): 后台配置、测试连接、Webhook 接收与路由"
```

---

### Task 10: 前端「MarsWallex 对接」页

**Files:**
- Create: `frontend/src/views/admin/MarsWallexIntegration.vue`
- Modify: `frontend/src/router/index.ts`（`OPS_BASE` children 中 `integration` 之后加 `{ path: 'marswallex', name: 'MarsWallexIntegration', component: () => import('../views/admin/MarsWallexIntegration.vue') }`）
- Modify: `frontend/src/layouts/AdminLayout.vue`（`navItems` 中「卡台接入」之后加 `{ path: '/ops/marswallex', label: 'MarsWallex', icon: 'Wallet' }`）
- Modify: `frontend/src/element.ts`（图标导入列表加 `Wallet`，并按该文件现有方式注册）

**Interfaces:**
- Consumes: Task 9 四个后台接口及其 JSON 约定；`authFetch`（`frontend/src/lib/api.ts`）

页面 4 块，样式类与 `CardIntegration.vue` 一致（`btn-primary`、`btn-secondary` 等）：
1. **环境与地址**：两个预设按钮 + 地址输入框。
2. **凭证**：developerId 输入框；apiToken、私钥、平台公钥为「只写」输入（已配置时 placeholder 显示「已配置，留空不修改」）；私钥、平台公钥用多行文本框；显示我们的公钥 PEM（带「复制」按钮）和两个指纹；`master_key_ready=false` 时顶部显示红色提示「服务器未配置 APP_MASTER_KEY，无法保存密钥」并禁用保存。保存成功后清空密钥输入框并刷新配置。
3. **测试连接**：按钮；结果显示 outcome（成功绿色、其余橙/红）、hint、HTTP 状态；成功时用表格列出账户和各币种可用/冻结余额。
4. **Webhook**：显示 `webhook_url`（带「复制」按钮，并提示「在 MarsWallex 开发者平台配置此地址」）；最近事件表（时间、topic、消息 ID、处理状态），带刷新按钮。

- [ ] **Step 1: 安装依赖（node_modules 当前未安装）**

Run: `cd frontend && npm ci`
Expected: 安装完成，无 ERR

- [ ] **Step 2: 实现页面、路由、导航、图标注册**

- [ ] **Step 3: 构建验证**

Run: `cd frontend && npm run build`
Expected: `vite build` 成功，无类型或编译错误

- [ ] **Step 4: 提交**

```bash
git add frontend/src/views/admin/MarsWallexIntegration.vue frontend/src/router/index.ts frontend/src/layouts/AdminLayout.vue frontend/src/element.ts
git commit -m "feat(ops): MarsWallex 对接页（凭证、测试连接、Webhook）"
```

---

### Task 11: 全量验证

**Files:** 无改动（如发现问题，回到对应任务修复并单独提交）

- [ ] **Step 1: 后端测试**

Run: `cd backend && go test ./... 2>&1 | tail -30`
Expected: 除 `internal/handler` 中已知的 `TestIssueProductKeepsGPTDefault`、`TestXIssueInfersProductWithoutQueryParameter` 外全部 PASS；`internal/secretbox`、`internal/marswallex`、`internal/db` 均 `ok`。用 `go test ./internal/handler/ -run TestMarsWallex` 单独确认新增 handler 测试为 `ok`。

- [ ] **Step 2: 静态检查**

Run: `cd backend && go vet ./...`
Expected: 无输出

- [ ] **Step 3: 前端构建**

Run: `cd frontend && npm run build`
Expected: 成功

- [ ] **Step 4: 密钥泄露检查**

Run: `git diff master...HEAD | grep -nE 'BEGIN (RSA )?PRIVATE KEY' || echo none`
Expected: `none`（测试中的密钥都应运行时生成，不得提交 PEM 私钥）

- [ ] **Step 5: 交给用户做生产只读验收**（规范 §10.2 第 3、4 项）：部署后在 `/ops/marswallex` 录入生产凭证 → 点「测试连接」应为「连接成功」并显示余额；在 MarsWallex 平台配置回调地址后，Webhook 列表出现事件。
