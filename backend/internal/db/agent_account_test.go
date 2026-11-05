package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

var acctNow = time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)

// newTestAgent 建一个等级和一个代理商，返回代理商 ID。
func newTestAgent(t *testing.T, username string) int64 {
	t.Helper()
	ctx := context.Background()
	var tierID int64
	if ts, err := db.ListAgentTiers(ctx); err == nil && len(ts) > 0 {
		tierID = ts[0].ID
	} else {
		tierID, err = db.CreateAgentTier(ctx, "默认", "", acctNow)
		if err != nil {
			t.Fatal(err)
		}
	}
	id, err := db.CreateAgent(ctx, db.NewAgent{Username: username, DisplayName: "测试", PasswordHash: "hash0", TierID: tierID}, acctNow)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCreateAgentUsernameCaseInsensitive(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	tiers, _ := db.ListAgentTiers(ctx)
	if _, err := db.CreateAgent(ctx, db.NewAgent{Username: "Alice", PasswordHash: "h", TierID: tiers[0].ID}, acctNow); !errors.Is(err, db.ErrAgentUsernameTaken) {
		t.Fatalf("期望 ErrAgentUsernameTaken，得到 %v", err)
	}
	a, sec, err := db.GetAgentByUsername(ctx, "ALICE")
	if err != nil || a.ID != id || a.Username != "alice" || sec.PasswordHash != "hash0" {
		t.Fatalf("按登录名取号: %+v %+v %v", a, sec, err)
	}
	if !a.MustChangePassword || a.SessionVersion != 1 || a.BalanceCents != 0 || a.Status != db.AgentActive || a.TierName != "默认" || a.TOTPBound || a.LastLoginAt != nil {
		t.Fatalf("初始状态不对: %+v", a)
	}
	if _, err := db.CreateAgent(ctx, db.NewAgent{Username: "bob", PasswordHash: "h", TierID: 9999}, acctNow); !errors.Is(err, db.ErrAgentTierNotFound) {
		t.Fatalf("等级不存在应报 ErrAgentTierNotFound，得到 %v", err)
	}
	if _, _, err := db.GetAgentByUsername(ctx, "nobody"); !errors.Is(err, db.ErrAgentNotFound) {
		t.Fatalf("期望 ErrAgentNotFound，得到 %v", err)
	}
	// Task 1 的约束：余额不能为负
	if _, err := db.DB.Exec(`UPDATE agents SET balance_cents = -1 WHERE id = ?`, id); err == nil {
		t.Fatal("balance_cents < 0 应被 CHECK 拒绝")
	}
}

func TestAgentProfileStatusListAndLogin(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id1 := newTestAgent(t, "a1")
	id2 := newTestAgent(t, "a2")
	tier2, _ := db.CreateAgentTier(ctx, "金牌", "", acctNow)
	if err := db.UpdateAgentProfile(ctx, id1, "新名字", tier2, "备注", acctNow); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateAgentProfile(ctx, id1, "x", 9999, "", acctNow); !errors.Is(err, db.ErrAgentTierNotFound) {
		t.Fatalf("got %v", err)
	}
	if err := db.UpdateAgentProfile(ctx, 9999, "x", tier2, "", acctNow); !errors.Is(err, db.ErrAgentNotFound) {
		t.Fatalf("got %v", err)
	}
	a, _ := db.GetAgent(ctx, id1)
	if a.DisplayName != "新名字" || a.TierID != tier2 || a.TierName != "金牌" || a.Note != "备注" {
		t.Fatalf("%+v", a)
	}
	// 停用加 1，启用不加
	if err := db.SetAgentStatus(ctx, id1, db.AgentDisabled, acctNow); err != nil {
		t.Fatal(err)
	}
	a, _ = db.GetAgent(ctx, id1)
	if a.Status != db.AgentDisabled || a.SessionVersion != 2 {
		t.Fatalf("%+v", a)
	}
	if err := db.SetAgentStatus(ctx, id1, db.AgentActive, acctNow); err != nil {
		t.Fatal(err)
	}
	a, _ = db.GetAgent(ctx, id1)
	if a.Status != db.AgentActive || a.SessionVersion != 2 {
		t.Fatalf("%+v", a)
	}
	if err := db.SetAgentStatus(ctx, id1, "weird", acctNow); err == nil {
		t.Fatal("非法状态应报错")
	}
	if err := db.TouchAgentLogin(ctx, id2, acctNow); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListAgents(ctx)
	if err != nil || len(list) != 2 || list[0].ID != id2 || list[1].ID != id1 {
		t.Fatalf("应按 id 降序: %+v %v", list, err)
	}
	if list[0].LastLoginAt == nil || *list[0].LastLoginAt != acctNow.Unix() {
		t.Fatalf("最后登录时间: %+v", list[0].LastLoginAt)
	}
	// 本站码计数：只算 agent_id 非 0 的
	for i, aid := range []int64{id1, id1, id2, 0} {
		_, err := db.DB.Exec(`INSERT INTO local_cdks (provider, code_hash, token_hash, code_prefix, product, plan_key, region, created_at, updated_at, agent_id)
			VALUES ('marswallex', ?, ?, 'ZC', 'gpt', 'plus', 'PH', 1, 1, ?)`, "ch"+string(rune('a'+i)), "th"+string(rune('a'+i)), aid)
		if err != nil {
			t.Fatal(err)
		}
	}
	counts, err := db.CountAgentCodes(ctx)
	if err != nil || len(counts) != 2 || counts[id1] != 2 || counts[id2] != 1 {
		t.Fatalf("%v %v", counts, err)
	}
}

func TestSetAgentPasswordBumpsSessionVersion(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	sv, err := db.SetAgentPassword(ctx, id, "hash1", false, acctNow)
	if err != nil || sv != 2 {
		t.Fatalf("sv=%d err=%v", sv, err)
	}
	a, _ := db.GetAgent(ctx, id)
	sec, _ := db.GetAgentSecrets(ctx, id)
	if a.SessionVersion != 2 || a.MustChangePassword || sec.PasswordHash != "hash1" {
		t.Fatalf("%+v %+v", a, sec)
	}
	if sv, _ = db.SetAgentPassword(ctx, id, "hash2", true, acctNow); sv != 3 {
		t.Fatalf("sv=%d", sv)
	}
	a, _ = db.GetAgent(ctx, id)
	if !a.MustChangePassword {
		t.Fatal("重置后应要求改密码")
	}
	if _, err := db.SetAgentPassword(ctx, 9999, "h", false, acctNow); !errors.Is(err, db.ErrAgentNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestResetAgentTOTPClearsAndBumps(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	if err := db.BindAgentTOTP(ctx, id, "sealed1", 50, acctNow); err != nil {
		t.Fatal(err)
	}
	if err := db.BindAgentTOTP(ctx, id, "sealed2", 51, acctNow); !errors.Is(err, db.ErrAgentTOTPAlreadyBound) {
		t.Fatalf("已绑定时应拒绝，得到 %v", err)
	}
	if err := db.BindAgentTOTP(ctx, 9999, "s", 1, acctNow); !errors.Is(err, db.ErrAgentNotFound) {
		t.Fatalf("got %v", err)
	}
	a, _ := db.GetAgent(ctx, id)
	sec, _ := db.GetAgentSecrets(ctx, id)
	if !a.TOTPBound || sec.TOTPSealed != "sealed1" || sec.TOTPLastStep != 50 {
		t.Fatalf("%+v %+v", a, sec)
	}
	if err := db.ResetAgentTOTP(ctx, id, acctNow); err != nil {
		t.Fatal(err)
	}
	a, _ = db.GetAgent(ctx, id)
	sec, _ = db.GetAgentSecrets(ctx, id)
	if a.TOTPBound || sec.TOTPSealed != "" || sec.TOTPLastStep != 0 || a.SessionVersion != 2 {
		t.Fatalf("%+v %+v", a, sec)
	}
	if err := db.BindAgentTOTP(ctx, id, "sealed3", 60, acctNow); err != nil {
		t.Fatalf("重置后应能重新绑定: %v", err)
	}
}

func TestUseAgentTOTPStepRejectsReplay(t *testing.T) {
	dbtest.Open(t)
	ctx := context.Background()
	id := newTestAgent(t, "alice")
	for _, c := range []struct {
		step int64
		want bool
	}{{100, true}, {100, false}, {99, false}, {101, true}} {
		got, err := db.UseAgentTOTPStep(ctx, id, c.step)
		if err != nil || got != c.want {
			t.Fatalf("step %d: got %v err %v, want %v", c.step, got, err, c.want)
		}
	}
}
