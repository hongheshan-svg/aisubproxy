package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

const sealedSwapPassword = "swap-secret-sealed"

// newSealedExchangeEnv 照 TestAgentExchangeLocalCDK 准备换码环境：过了硬门槛、设好换码密码、清空换码限流。
func newSealedExchangeEnv(t *testing.T) *localEnv {
	t.Helper()
	e := newLocalEnv(t)
	e.seedRealPayment()
	if err := setAgentSwapPassword(sealedSwapPassword); err != nil {
		t.Fatal(err)
	}
	swapRateMu.Lock()
	swapRateHits = map[string][]time.Time{}
	swapRateMu.Unlock()
	return e
}

// exchangeableCode 走后台发一张码，再写一张注资后失败的单，让它符合换码条件。
func exchangeableCode(t *testing.T, e *localEnv) (code string, id int64) {
	t.Helper()
	code, id = e.issueCode("plus", "PH")
	e.insertFundedFailure(id, "a@example.com")
	return code, id
}

// doSealedExchange 走公开的代理换码接口，要求 200，返回响应。
func doSealedExchange(t *testing.T, e *localEnv, code string) (m map[string]any, newCode string, newID int64) {
	t.Helper()
	w := e.do("POST", "/public/cdk/exchange", localBody(t, map[string]any{"password": sealedSwapPassword, "code": code}))
	if w.Code != http.StatusOK {
		t.Fatalf("换码应成功: %d %s", w.Code, w.Body.String())
	}
	m = localJSON(t, w)
	newCode, _ = m["new_code"].(string)
	if !strings.HasPrefix(newCode, "MW-") {
		t.Fatalf("响应应带新码: %v", m)
	}
	id, _ := m["new_cdk_id"].(float64)
	if id <= 0 {
		t.Fatalf("响应应带新码 id: %v", m)
	}
	return m, newCode, int64(id)
}

// storedSealed 直接读库里这一行的 code_sealed。
func storedSealed(t *testing.T, id int64) string {
	t.Helper()
	var s string
	if err := db.DB.QueryRow(`SELECT code_sealed FROM local_cdks WHERE id = ?`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// 换码换出的新码用主密钥加密存一份，按新码 id 能解回同一张码；批量发出的旧码不保存完整码。
func TestExchangeSealsNewCode(t *testing.T) {
	e := newSealedExchangeEnv(t) // newLocalEnv 已设好主密钥
	ctx := context.Background()
	code, oldID := exchangeableCode(t, e)
	_, newCode, newID := doSealedExchange(t, e, code)

	got, ok := openLocalCDKCode(ctx, newID)
	if !ok || got != newCode {
		t.Fatalf("应解回响应里的新码: ok=%v 一致=%v", ok, got == newCode)
	}
	sealed := storedSealed(t, newID)
	if !strings.HasPrefix(sealed, "v1:") || strings.Contains(sealed, newCode) {
		t.Fatalf("库里应是密文: %q", sealed)
	}
	if s := storedSealed(t, oldID); s != "" {
		t.Fatalf("后台批量发的码不保存完整码: %q", s)
	}
	if _, ok := openLocalCDKCode(ctx, oldID); ok {
		t.Fatal("没有密文的码不能解出东西")
	}
	if _, ok := openLocalCDKCode(ctx, 99999); ok {
		t.Fatal("不存在的码不能解出东西")
	}
}

// 主密钥缺失或不合法时换码照常完成：新码不加密保存，查不回原文，按天告警。
func TestExchangeWithoutMasterKeyStillWorks(t *testing.T) {
	for _, tc := range []struct{ name, key string }{
		{"缺失", ""},
		{"不合法", "not-a-valid-master-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSealedExchangeEnv(t)
			code, _ := exchangeableCode(t, e) // 发码要读加密的 MarsWallex 配置，先发码再清主密钥
			t.Setenv("APP_MASTER_KEY", tc.key)
			_, _, newID := doSealedExchange(t, e, code)
			if s := storedSealed(t, newID); s != "" {
				t.Fatalf("没有主密钥时 code_sealed 应为空: %q", s)
			}
			if _, ok := openLocalCDKCode(context.Background(), newID); ok {
				t.Fatal("没有密文时不能解出东西")
			}
			if a := e.alertTexts(); !strings.Contains(a, "APP_MASTER_KEY") || !strings.Contains(a, "本站码换出的新码没有加密保存") {
				t.Fatalf("应告警主密钥问题: %q", a)
			}
		})
	}
}

// 密文被挪动、换了主密钥、被改动，或解开的码与这一行的哈希对不上，一律解不出。
func TestOpenLocalCDKCodeRejectsTampered(t *testing.T) {
	const codeA, codeB = "MW-AAAAA-BBBBB-CCCCC-DDDDD-EEEEE", "MW-FFFFF-GGGGG-HHHHH-JJJJJ-KKKKK"
	ctx := context.Background()
	insert := func(t *testing.T, i int, code string) int64 {
		t.Helper()
		hash := db.HashCDKCode(code)
		sealed := sealLocalCDKCode(code, hash)
		if !strings.HasPrefix(sealed, "v1:") {
			t.Fatalf("应加密成功: %q", sealed)
		}
		ids, err := db.InsertLocalCDKs(ctx, fmt.Sprintf("sealed-tamper-%d", i), []db.NewLocalCDK{{
			Provider: "marswallex", CodeHash: hash, TokenHash: fmt.Sprintf("sealed-token-%d", i), CodePrefix: code[:8],
			Product: "gpt", PlanKey: "plus", Region: "PH", IssuedBy: "agent-swap", CodeSealed: sealed,
		}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return ids[0]
	}
	setup := func(t *testing.T) (idA, idB int64) {
		t.Helper()
		dbtest.Open(t)
		dbtest.SetMasterKey(t)
		idA, idB = insert(t, 1, codeA), insert(t, 2, codeB)
		for id, want := range map[int64]string{idA: codeA, idB: codeB} {
			if got, ok := openLocalCDKCode(ctx, id); !ok || got != want {
				t.Fatalf("未改动时应能解开: id=%d ok=%v got=%q", id, ok, got)
			}
		}
		return idA, idB
	}
	setSealed := func(t *testing.T, id int64, sealed string) {
		t.Helper()
		if _, err := db.DB.Exec(`UPDATE local_cdks SET code_sealed = ? WHERE id = ?`, sealed, id); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("A 行密文拷到 B 行", func(t *testing.T) {
		idA, idB := setup(t)
		setSealed(t, idB, storedSealed(t, idA))
		if got, ok := openLocalCDKCode(ctx, idB); ok || got != "" {
			t.Fatalf("挪到别的行的密文不能解开: ok=%v got=%q", ok, got)
		}
	})
	t.Run("换一个主密钥", func(t *testing.T) {
		idA, _ := setup(t)
		t.Setenv("APP_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
		if got, ok := openLocalCDKCode(ctx, idA); ok || got != "" {
			t.Fatalf("换了主密钥不能解开: ok=%v got=%q", ok, got)
		}
	})
	t.Run("密文改掉一个字符", func(t *testing.T) {
		idA, _ := setup(t)
		b := []byte(storedSealed(t, idA))
		i := len(b) / 2
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		setSealed(t, idA, string(b))
		if got, ok := openLocalCDKCode(ctx, idA); ok || got != "" {
			t.Fatalf("改动过的密文不能解开: ok=%v got=%q", ok, got)
		}
	})
	t.Run("密文能解开但码与这一行的哈希不符", func(t *testing.T) {
		_, idB := setup(t)
		// AAD 用的是 B 行的哈希，能解开，但解出的是 A 码
		setSealed(t, idB, sealLocalCDKCode(codeA, db.HashCDKCode(codeB)))
		if got, ok := openLocalCDKCode(ctx, idB); ok || got != "" {
			t.Fatalf("码与哈希不符时不能交出去: ok=%v got=%q", ok, got)
		}
	})
}

// 后台码列表和导出接口的响应里没有 code_sealed、密文和新码原文；换码过程中的日志、审计、告警里也都没有。
func TestSealedCodeNotInAdminOutputs(t *testing.T) {
	logs := captureLog(t)
	e := newSealedExchangeEnv(t)
	e.router.GET("/admin/cardplatform/cdks/stored", CardPlatformListStoredCDKs) // 后台「导出本站全部」走的接口
	code, _ := exchangeableCode(t, e)
	_, newCode, newID := doSealedExchange(t, e, code)
	sealed := storedSealed(t, newID)
	if !strings.HasPrefix(sealed, "v1:") {
		t.Fatalf("前提：新码应已加密保存: %q", sealed)
	}
	leaks := func(where, s string) {
		t.Helper()
		for label, bad := range map[string]string{
			"code_sealed 字段": "code_sealed", "密文": sealed, "密文正文": strings.TrimPrefix(sealed, "v1:"), "新码原文": newCode,
		} {
			if strings.Contains(s, bad) {
				t.Errorf("%s 里出现了%s", where, label)
			}
		}
	}

	w := e.do("GET", "/admin/cdks?page_size=200", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), fmt.Sprintf(`"id":%d`, newID)) {
		t.Fatalf("后台码列表应列出新码: %d %s", w.Code, w.Body.String())
	}
	leaks("后台码列表", w.Body.String())
	for _, path := range []string{"/admin/cardplatform/cdks/stored?format=txt", "/admin/cardplatform/cdks/stored?sync=0"} {
		w := e.do("GET", path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		leaks("导出 "+path, w.Body.String())
	}
	leaks("标准库日志", logs.String())
	leaks("审计", localAudit(t))
	leaks("告警", e.alertTexts())
}
