package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

func zovoSeedKey(t *testing.T, code, status string) {
	t.Helper()
	if _, err := db.DB.Exec(`INSERT INTO cd_keys (code, plan_type, status) VALUES (?, 'plus', ?)`, code, status); err != nil {
		t.Fatal(err)
	}
}

func zovoSeedTask(t *testing.T, code, status, email, notes string) {
	t.Helper()
	if _, err := db.DB.Exec(`INSERT INTO recharge_tasks (task_id, cdk_code, account_email, task_status, notes) VALUES (?, ?, ?, ?, ?)`,
		"task-"+code+"-"+status, code, email, status, notes); err != nil {
		t.Fatal(err)
	}
}

func zovoSessionJSON(email string) string {
	b, _ := json.Marshal(map[string]any{"sessionToken": cdkSessToken, "user": map[string]string{"email": email}})
	return string(b)
}

// zovoSeedExchange 造一条换码记录并推进到指定状态：pending / disabling / done。
func zovoSeedExchange(t *testing.T, code, state string, newID int64, newPrefix string) {
	t.Helper()
	id, err := db.ReserveAgentCDKExchange(db.AgentExchangeReservation{
		OldHash: db.HashCDKCode(provider.NormalizeCode(code)), OldPrefix: "ZC-OLD", Plan: "plus", PasswordFP: "fp",
	}, 20, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if state == db.AgentExchangePending {
		return
	}
	if err := db.FinishAgentCDKExchange(id, newID, newPrefix); err != nil {
		t.Fatal(err)
	}
	if state == db.AgentExchangeDone {
		if err := db.CompleteAgentCDKExchangeDisable(id); err != nil {
			t.Fatal(err)
		}
	}
}

func zovoSetResult(u *cdkUpstream, status, email string, code int) {
	u.mu.Lock()
	u.resultStatus, u.resultEmail, u.resultCode = status, email, code
	u.mu.Unlock()
}

func TestZovoFactsEmailMaskedAllSources(t *testing.T) {
	const (
		taskEmail   = "task.user@example.com"
		bindEmail   = "bind.user@example.com"
		remoteEmail = "remote.user@example.com"
	)
	assertMasked := func(t *testing.T, r cdkLookupResult, want string) {
		t.Helper()
		raw, _ := json.Marshal(r)
		for _, full := range []string{taskEmail, bindEmail, remoteEmail} {
			if strings.Contains(string(raw), full) {
				t.Fatalf("响应里出现完整邮箱 %s: %s", full, raw)
			}
		}
		if r.AccountEmail != want {
			t.Fatalf("邮箱应是 %q: %+v", want, r)
		}
	}

	t.Run("recharge_tasks", func(t *testing.T) {
		dbtest.Open(t)
		zovoSeedKey(t, cdkSessCode, "active")
		zovoSeedTask(t, cdkSessCode, "completed", taskEmail, "")
		assertMasked(t, lookupOneCDK(context.Background(), cdkSessCode, ""), "ta***@example.com")
	})
	t.Run("绑定记录", func(t *testing.T) {
		dbtest.Open(t)
		dbtest.SetMasterKey(t)
		newCDKUpstream(t)
		if err := db.BindCDKSession("zovocard", cdkSessCode, "rt-1", zovoSessionJSON(bindEmail)); err != nil {
			t.Fatal(err)
		}
		assertMasked(t, lookupOneCDK(context.Background(), cdkSessCode, ""), "bi***@example.com")
	})
	t.Run("卡台 Result", func(t *testing.T) {
		dbtest.Open(t)
		dbtest.SetMasterKey(t)
		u := newCDKUpstream(t)
		zovoSetResult(u, "completed", remoteEmail, 0)
		if err := db.BindCDKSession("zovocard", cdkSessCode, "rt-1", zovoSessionJSON(bindEmail)); err != nil {
			t.Fatal(err)
		}
		assertMasked(t, lookupOneCDK(context.Background(), cdkSessCode, ""), "re***@example.com")
	})
}

func TestZovoFactsFailureGuide(t *testing.T) {
	const note = "Session 无效，请重新获取"
	t.Run("可以重新提交", func(t *testing.T) {
		dbtest.Open(t)
		zovoSeedKey(t, cdkSessCode, "active")
		zovoSeedTask(t, cdkSessCode, "failed", "", note)
		f, err := zovoCardFacts(context.Background(), cdkSessCode, "")
		if err != nil || !f.Found || f.Status != "failed" || !f.Reusable || f.Guide != "provider_retry" || f.ProviderNote != note {
			t.Fatalf("%+v %v", f, err)
		}
		r := lookupOneCDK(context.Background(), cdkSessCode, "")
		if r.Guide != "provider_retry" || !r.CanResubmit || r.ProviderNote != note {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("还不能重新提交", func(t *testing.T) {
		dbtest.Open(t)
		zovoSeedKey(t, cdkSessCode, "expired")
		zovoSeedTask(t, cdkSessCode, "failed", "", note)
		f, err := zovoCardFacts(context.Background(), cdkSessCode, "")
		if err != nil || f.Status != "failed" || f.Reusable || f.Guide != "provider_wait" || f.ProviderNote != note {
			t.Fatalf("%+v %v", f, err)
		}
		r := lookupOneCDK(context.Background(), cdkSessCode, "")
		if r.Guide != "provider_wait" || r.CanResubmit {
			t.Fatalf("%+v", r)
		}
	})
}

func TestZovoFactsReplacement(t *testing.T) {
	const newFull = "ZC-NEWCODE-FULL-0002"
	t.Run("done 且库里有新码", func(t *testing.T) {
		dbtest.Open(t)
		zovoSeedKey(t, cdkSessCode, "disabled")
		zovoSeedExchange(t, cdkSessCode, db.AgentExchangeDone, 900, "ZC-NEWCO")
		if _, err := db.DB.Exec(`INSERT INTO cardplatform_cdk_codes (upstream_id, code, code_prefix) VALUES (900, ?, 'ZC-NEWCO')`, newFull); err != nil {
			t.Fatal(err)
		}
		r := lookupOneCDK(context.Background(), cdkSessCode, "")
		if r.Status != "replaced" || r.Replacement == nil || r.Replacement.Code != newFull || r.ReplacedUnknown {
			t.Fatalf("%+v", r)
		}
		if r.Message != afterSaleReplacedText {
			t.Fatalf("换码时文字应是默认的换码文字，不是「已禁用」: %q", r.Message)
		}
	})
	t.Run("done 但库里没有新码", func(t *testing.T) {
		dbtest.Open(t)
		zovoSeedKey(t, cdkSessCode, "disabled")
		zovoSeedExchange(t, cdkSessCode, db.AgentExchangeDone, 901, "ZC-GONE")
		r := lookupOneCDK(context.Background(), cdkSessCode, "")
		if r.Status != "replaced" || r.Replacement != nil || !r.ReplacedUnknown {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("pending 不算换码", func(t *testing.T) {
		dbtest.Open(t)
		zovoSeedKey(t, cdkSessCode, "active")
		zovoSeedExchange(t, cdkSessCode, db.AgentExchangePending, 0, "")
		r := lookupOneCDK(context.Background(), cdkSessCode, "")
		if r.Status != "unused" || r.Replacement != nil || r.ReplacedUnknown {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("disabling 不算换码", func(t *testing.T) {
		dbtest.Open(t)
		zovoSeedKey(t, cdkSessCode, "active")
		zovoSeedExchange(t, cdkSessCode, db.AgentExchangeDisabling, 902, "ZC-DIS")
		r := lookupOneCDK(context.Background(), cdkSessCode, "")
		if r.Status != "unused" || r.Replacement != nil || r.ReplacedUnknown {
			t.Fatalf("%+v", r)
		}
	})
}

func TestZovoFactsNotFound(t *testing.T) {
	dbtest.Open(t)
	f, err := zovoCardFacts(context.Background(), cdkSessCode, "")
	if err != nil || f.Found {
		t.Fatalf("三处都没有应是 Found=false 且无 error: %+v %v", f, err)
	}
}

func TestZovoFactsDBErrorIsError(t *testing.T) {
	for _, table := range []string{"cd_keys", "cardplatform_cdk_codes", "recharge_tasks", "agent_cdk_exchanges"} {
		t.Run(table, func(t *testing.T) {
			dbtest.Open(t)
			zovoSeedKey(t, cdkSessCode, "active")
			if _, err := db.DB.Exec(`ALTER TABLE ` + table + ` RENAME TO ` + table + `_gone`); err != nil {
				t.Fatal(err)
			}
			f, err := zovoCardFacts(context.Background(), cdkSessCode, "")
			if err == nil || f.Found {
				t.Fatalf("读库出错应返回 error: %+v %v", f, err)
			}
			r := lookupOneCDK(context.Background(), cdkSessCode, "")
			if r.Status != "unknown" || r.Message != afterSaleUnknownText || !r.queryFailed {
				t.Fatalf("经公共层应是「暂时查不到」: %+v", r)
			}
		})
	}
}

func TestZovoFactsUnknownKeyStatusIsError(t *testing.T) {
	dbtest.Open(t)
	zovoSeedKey(t, cdkSessCode, "weird-state-x")
	f, err := zovoCardFacts(context.Background(), cdkSessCode, "")
	if err == nil || f.Found {
		t.Fatalf("不认得的状态应返回 error: %+v %v", f, err)
	}
	if strings.Contains(err.Error(), "weird") {
		t.Fatalf("错误文字不能带原始状态: %v", err)
	}
	r := lookupOneCDK(context.Background(), cdkSessCode, "")
	raw, _ := json.Marshal(r)
	if r.Status != "unknown" || r.Message != afterSaleUnknownText || strings.Contains(string(raw), "weird") {
		t.Fatalf("%s", raw)
	}
}

func TestZovoFactsRemoteFailure(t *testing.T) {
	setup := func(t *testing.T) {
		t.Helper()
		dbtest.Open(t)
		dbtest.SetMasterKey(t)
		u := newCDKUpstream(t)
		zovoSetResult(u, "running", "", 500)
		if err := db.BindCDKSession("zovocard", cdkSessCode, "rt-1", cdkSessJSON()); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("本站库什么都没有：只能靠卡台，失败就是 error", func(t *testing.T) {
		setup(t)
		f, err := zovoCardFacts(context.Background(), cdkSessCode, "")
		if err == nil || f.Found {
			t.Fatalf("%+v %v", f, err)
		}
		if r := lookupOneCDK(context.Background(), cdkSessCode, ""); r.Message != afterSaleUnknownText || !r.queryFailed {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("本站库已能确定：照旧用本站数据", func(t *testing.T) {
		setup(t)
		zovoSeedKey(t, cdkSessCode, "disabled")
		f, err := zovoCardFacts(context.Background(), cdkSessCode, "")
		if err != nil || !f.Found || f.Status != "disabled" {
			t.Fatalf("%+v %v", f, err)
		}
	})
}

func TestZovoFactsStatusMapping(t *testing.T) {
	for _, tc := range []struct{ key, status string }{
		{"active", "unused"}, {"", "unused"}, {"used", "used"}, {"disabled", "disabled"}, {"expired", "expired"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			dbtest.Open(t)
			zovoSeedKey(t, cdkSessCode, tc.key)
			f, err := zovoCardFacts(context.Background(), cdkSessCode, "")
			if err != nil || !f.Found || f.Status != tc.status || f.ExpiresAt != nil || f.PlanKind != "" {
				t.Fatalf("%+v %v", f, err)
			}
		})
	}
}

func TestZovoFactsFailedThenCompletedClearsFailure(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	u := newCDKUpstream(t)
	zovoSetResult(u, "completed", "", 0)
	zovoSeedKey(t, cdkSessCode, "active")
	zovoSeedTask(t, cdkSessCode, "failed", "", "Session 无效，请重新获取")
	if err := db.BindCDKSession("zovocard", cdkSessCode, "rt-1", cdkSessJSON()); err != nil {
		t.Fatal(err)
	}
	r := lookupOneCDK(context.Background(), cdkSessCode, "")
	if r.Status != "used" || r.CanResubmit || r.Guide != "" || r.ProviderNote != "" || r.Notes != "" {
		t.Fatalf("已完成不该带失败的指引和原文: %+v", r)
	}
}

func TestZovoFactsReplacementIsStrictByUpstreamID(t *testing.T) {
	const other = "ZC-NEWCO-OTHER-CUSTOMER-9"
	t.Run("同前缀的别人的码不能给出去", func(t *testing.T) {
		dbtest.Open(t)
		zovoSeedKey(t, cdkSessCode, "disabled")
		zovoSeedExchange(t, cdkSessCode, db.AgentExchangeDone, 900, "ZC-NEWCO")
		if _, err := db.DB.Exec(`INSERT INTO cardplatform_cdk_codes (upstream_id, code, code_prefix) VALUES (555, ?, 'ZC-NEWCO')`, other); err != nil {
			t.Fatal(err)
		}
		r := lookupOneCDK(context.Background(), cdkSessCode, "")
		raw, _ := json.Marshal(r)
		if r.Status != "replaced" || r.Replacement != nil || !r.ReplacedUnknown || strings.Contains(string(raw), other) {
			t.Fatalf("%s", raw)
		}
	})
	t.Run("NewUpstreamID 为 0", func(t *testing.T) {
		dbtest.Open(t)
		zovoSeedKey(t, cdkSessCode, "disabled")
		zovoSeedExchange(t, cdkSessCode, db.AgentExchangeDone, 0, "ZC-NEWCO")
		if _, err := db.DB.Exec(`INSERT INTO cardplatform_cdk_codes (upstream_id, code, code_prefix) VALUES (555, ?, 'ZC-NEWCO')`, other); err != nil {
			t.Fatal(err)
		}
		r := lookupOneCDK(context.Background(), cdkSessCode, "")
		raw, _ := json.Marshal(r)
		if r.Status != "replaced" || r.Replacement != nil || !r.ReplacedUnknown || strings.Contains(string(raw), other) {
			t.Fatalf("%s", raw)
		}
	})
}

func TestZovoFactsRemoteEmptyStatusIsError(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	u := newCDKUpstream(t)
	zovoSetResult(u, "", "", 0)
	if err := db.BindCDKSession("zovocard", cdkSessCode, "rt-1", cdkSessJSON()); err != nil {
		t.Fatal(err)
	}
	f, err := zovoCardFacts(context.Background(), cdkSessCode, "")
	if err == nil || f.Found {
		t.Fatalf("卡台没给出订单状态、本站又没有状态，应是 error: %+v %v", f, err)
	}
}

func TestZovoFactsBindingErrorIsError(t *testing.T) {
	dbtest.Open(t)
	zovoSeedKey(t, cdkSessCode, "active")
	if _, err := db.DB.Exec(`ALTER TABLE cdk_session_bindings RENAME TO cdk_session_bindings_gone`); err != nil {
		t.Fatal(err)
	}
	r := lookupOneCDK(context.Background(), cdkSessCode, "")
	if r.Status != "unknown" || r.Message != afterSaleUnknownText || !r.queryFailed {
		t.Fatalf("%+v", r)
	}
}

func TestZovoFactsExchangeUnknownStateIsNotReplacement(t *testing.T) {
	dbtest.Open(t)
	zovoSeedKey(t, cdkSessCode, "active")
	id, err := db.ReserveAgentCDKExchange(db.AgentExchangeReservation{
		OldHash: db.HashCDKCode(provider.NormalizeCode(cdkSessCode)), OldPrefix: "ZC-OLD", Plan: "plus", PasswordFP: "fp",
	}, 20, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkAgentCDKExchangeUnknown(id, "超时"); err != nil {
		t.Fatal(err)
	}
	r := lookupOneCDK(context.Background(), cdkSessCode, "")
	if r.Status != "unused" || r.Replacement != nil || r.ReplacedUnknown {
		t.Fatalf("%+v", r)
	}
}

func TestZovoFactsUsedAtIsRFC3339UTC(t *testing.T) {
	dbtest.Open(t)
	zovoSeedKey(t, cdkSessCode, "used")
	if _, err := db.DB.Exec(`UPDATE cd_keys SET used_at = '2026-03-04 05:06:07' WHERE code = ?`, cdkSessCode); err != nil {
		t.Fatal(err)
	}
	r := lookupOneCDK(context.Background(), cdkSessCode, "")
	if r.UsedAt == nil || *r.UsedAt != "2026-03-04T05:06:07Z" {
		t.Fatalf("%v", r.UsedAt)
	}
	if _, err := time.Parse(time.RFC3339, *r.UsedAt); err != nil {
		t.Fatal(err)
	}
}

func TestZovoFactsActiveButPastExpiryIsExpired(t *testing.T) {
	dbtest.Open(t)
	zovoSeedKey(t, cdkSessCode, "active")
	if _, err := db.DB.Exec(`UPDATE cd_keys SET expires_at = '2020-01-01 00:00:00' WHERE code = ?`, cdkSessCode); err != nil {
		t.Fatal(err)
	}
	r := lookupOneCDK(context.Background(), cdkSessCode, "")
	if r.Status != "expired" || r.Message != "卡密已过期" {
		t.Fatalf("%+v", r)
	}
}
