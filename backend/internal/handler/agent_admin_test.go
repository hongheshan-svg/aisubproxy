package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/agent"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

const adminBase = "/api/v1/admin"

func decodeMap(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("响应不是 JSON: %s", body)
	}
	return m
}

// adminCreateAgent 经接口建号，返回 id 和初始密码。
func (e *agentEnv) adminCreateAgent(username string, tier int64) (int64, string) {
	e.t.Helper()
	w := e.adminDo("POST", adminBase+"/agents", fmt.Sprintf(`{"username":%q,"display_name":"测试","tier_id":%d,"note":""}`, username, tier), nil)
	if w.Code != 200 {
		e.t.Fatalf("建号失败: %d %s", w.Code, w.Body.String())
	}
	m := decodeMap(e.t, w.Body.String())
	id := int64(m["agent"].(map[string]any)["id"].(float64))
	return id, m["password"].(string)
}

func TestAdminCreateAgentReturnsPasswordOnce(t *testing.T) {
	e := newAgentEnv(t)
	tier := e.seedTier("普通")
	w := e.adminDo("POST", adminBase+"/agents", fmt.Sprintf(`{"username":"alice","display_name":"爱丽丝","tier_id":%d,"note":"n"}`, tier), nil)
	if w.Code != 200 {
		t.Fatalf("建号: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("应带 no-store: %q", w.Header().Get("Cache-Control"))
	}
	m := decodeMap(t, w.Body.String())
	pw, _ := m["password"].(string)
	if len(pw) != 16 {
		t.Fatalf("初始密码应为 16 位: %q", pw)
	}
	ag := m["agent"].(map[string]any)
	if ag["username"] != "alice" || ag["tier_name"] != "普通" || ag["status"] != "active" || ag["must_change_password"] != true || ag["totp_bound"] != false {
		t.Fatalf("代理商对象不对: %v", ag)
	}
	id := int64(ag["id"].(float64))
	w = e.adminDo("GET", fmt.Sprintf("%s/agents/%d", adminBase, id), "", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), pw) || strings.Contains(w.Body.String(), "\"password\"") {
		t.Fatalf("详情不应带密码: %d %s", w.Code, w.Body.String())
	}
	w = e.adminDo("GET", adminBase+"/agents", "", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), pw) {
		t.Fatalf("列表不应带密码: %d %s", w.Code, w.Body.String())
	}
	var detail string
	if err := db.DB.QueryRow(`SELECT COALESCE(GROUP_CONCAT(detail, ' | '), '') FROM admin_audit_logs WHERE action = 'agent_create'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if detail == "" || strings.Contains(detail, pw) {
		t.Fatalf("审计缺失或含密码: %q", detail)
	}
	// 新密码能登录
	w = e.send("POST", agentBase+"/auth/login", `{"username":"ALICE","password":"`+pw+`"}`, nil, nil)
	if w.Code != 200 || jsonField(t, w.Body.String(), "next") != "change_password" {
		t.Fatalf("用初始密码登录: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminCreateAgentValidation(t *testing.T) {
	e := newAgentEnv(t)
	tier := e.seedTier("普通")
	for _, name := range []string{"ab", "has space", "中文名字", strings.Repeat("a", 33), ""} {
		w := e.adminDo("POST", adminBase+"/agents", fmt.Sprintf(`{"username":%q,"display_name":"x","tier_id":%d}`, name, tier), nil)
		if w.Code != 400 || jsonField(t, w.Body.String(), "error") != "invalid_request" {
			t.Fatalf("登录名 %q 应 400: %d %s", name, w.Code, w.Body.String())
		}
	}
	w := e.adminDo("POST", adminBase+"/agents", `{"username":"bob","display_name":"x","tier_id":9999}`, nil)
	if w.Code != 400 || jsonField(t, w.Body.String(), "error") != "invalid_request" {
		t.Fatalf("等级不存在应 400: %d %s", w.Code, w.Body.String())
	}
	w = e.adminDo("POST", adminBase+"/agents", `{bad`, nil)
	if w.Code != 400 {
		t.Fatalf("坏 JSON 应 400: %d", w.Code)
	}
	w = e.adminDo("GET", adminBase+"/agents/9999", "", nil)
	if w.Code != 404 || jsonField(t, w.Body.String(), "error") != "not_found" {
		t.Fatalf("不存在应 404: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminCreateAgentDuplicateUsername(t *testing.T) {
	e := newAgentEnv(t)
	tier := e.seedTier("普通")
	e.adminCreateAgent("alice", tier)
	w := e.adminDo("POST", adminBase+"/agents", fmt.Sprintf(`{"username":"Alice","display_name":"x","tier_id":%d}`, tier), nil)
	if w.Code != 409 || jsonField(t, w.Body.String(), "error") != "username_taken" {
		t.Fatalf("重名应 409 username_taken: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminUpdateDisableEnable(t *testing.T) {
	e := newAgentEnv(t)
	t1, t2 := e.seedTier("普通"), e.seedTier("高级")
	id, pw, secret := e.seedAgent("alice", t1)
	s := e.login("alice", pw, secret)
	w := e.adminDo("PUT", fmt.Sprintf("%s/agents/%d", adminBase, id), fmt.Sprintf(`{"display_name":"新名","tier_id":%d,"note":"备注"}`, t2), nil)
	if w.Code != 200 {
		t.Fatalf("改资料: %d %s", w.Code, w.Body.String())
	}
	ag := decodeMap(t, w.Body.String())["agent"].(map[string]any)
	if ag["display_name"] != "新名" || ag["tier_name"] != "高级" || ag["note"] != "备注" {
		t.Fatalf("改后不对: %v", ag)
	}
	w = e.adminDo("PUT", fmt.Sprintf("%s/agents/%d", adminBase, id), `{"display_name":"x","tier_id":9999,"note":""}`, nil)
	if w.Code != 400 {
		t.Fatalf("等级不存在应 400: %d", w.Code)
	}
	w = e.adminDo("PUT", adminBase+"/agents/9999", fmt.Sprintf(`{"display_name":"x","tier_id":%d,"note":""}`, t1), nil)
	if w.Code != 404 {
		t.Fatalf("代理商不存在应 404: %d", w.Code)
	}
	if w = e.agentDo(s, "GET", agentBase+"/me", ""); w.Code != 200 {
		t.Fatalf("停用前会话应有效: %d", w.Code)
	}
	w = e.adminDo("POST", fmt.Sprintf("%s/agents/%d/disable", adminBase, id), "", nil)
	if w.Code != 200 || decodeMap(t, w.Body.String())["agent"].(map[string]any)["status"] != "disabled" {
		t.Fatalf("停用: %d %s", w.Code, w.Body.String())
	}
	if w = e.agentDo(s, "GET", agentBase+"/me", ""); w.Code != 401 {
		t.Fatalf("停用后旧会话应 401: %d", w.Code)
	}
	w = e.adminDo("POST", fmt.Sprintf("%s/agents/%d/enable", adminBase, id), "", nil)
	if w.Code != 200 || decodeMap(t, w.Body.String())["agent"].(map[string]any)["status"] != "active" {
		t.Fatalf("启用: %d %s", w.Code, w.Body.String())
	}
	if w = e.agentDo(s, "GET", agentBase+"/me", ""); w.Code != 401 {
		t.Fatalf("启用不应让旧会话复活: %d", w.Code)
	}
	for _, act := range []string{"agent_update", "agent_disable", "agent_enable"} {
		var n int
		db.DB.QueryRow(`SELECT COUNT(*) FROM admin_audit_logs WHERE action = ?`, act).Scan(&n)
		if n != 1 {
			t.Fatalf("审计 %s 应有 1 条: %d", act, n)
		}
	}
}

func TestAdminResetPasswordForcesChange(t *testing.T) {
	e := newAgentEnv(t)
	tier := e.seedTier("普通")
	id, pw, secret := e.seedAgent("alice", tier)
	s := e.login("alice", pw, secret)
	w := e.adminDo("POST", fmt.Sprintf("%s/agents/%d/reset-password", adminBase, id), "", nil)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("重置: %d %v", w.Code, w.Header())
	}
	newPw := jsonField(t, w.Body.String(), "password")
	if len(newPw) != 16 || newPw == pw {
		t.Fatalf("新密码不对: %q", newPw)
	}
	if w = e.agentDo(s, "GET", agentBase+"/me", ""); w.Code != 401 {
		t.Fatalf("旧会话应 401: %d", w.Code)
	}
	w = e.send("POST", agentBase+"/auth/login", `{"username":"alice","password":"`+pw+`"}`, nil, nil)
	if w.Code != 401 {
		t.Fatalf("旧密码应登录失败: %d", w.Code)
	}
	w = e.send("POST", agentBase+"/auth/login", `{"username":"alice","password":"`+newPw+`"}`, nil, nil)
	if w.Code != 200 || jsonField(t, w.Body.String(), "next") != "change_password" {
		t.Fatalf("新密码登录: %d %s", w.Code, w.Body.String())
	}
	var detail string
	db.DB.QueryRow(`SELECT COALESCE(GROUP_CONCAT(detail, ' | '), '') FROM admin_audit_logs`).Scan(&detail)
	if strings.Contains(detail, newPw) {
		t.Fatalf("审计含新密码")
	}
	w = e.adminDo("POST", adminBase+"/agents/9999/reset-password", "", nil)
	if w.Code != 404 {
		t.Fatalf("不存在应 404: %d", w.Code)
	}
}

func TestAdminResetTOTPForcesRebind(t *testing.T) {
	e := newAgentEnv(t)
	tier := e.seedTier("普通")
	id, pw, secret := e.seedAgent("alice", tier)
	s := e.login("alice", pw, secret)
	w := e.adminDo("POST", fmt.Sprintf("%s/agents/%d/reset-totp", adminBase, id), "", nil)
	if w.Code != 200 {
		t.Fatalf("重置两步验证: %d %s", w.Code, w.Body.String())
	}
	if w = e.agentDo(s, "GET", agentBase+"/me", ""); w.Code != 401 {
		t.Fatalf("旧会话应 401: %d", w.Code)
	}
	e.clock = e.clock.Add(30e9)
	w = e.send("POST", agentBase+"/auth/login", `{"username":"alice","password":"`+pw+`"}`, nil, nil)
	if w.Code != 200 || jsonField(t, w.Body.String(), "next") != "setup_totp" {
		t.Fatalf("应进入 setup_totp: %d %s", w.Code, w.Body.String())
	}
	_ = agent.StageSetupTOTP
}

func adjust(e *agentEnv, id int64, key, body string) (int, map[string]any) {
	h := map[string]string{}
	if key != "" {
		h["Idempotency-Key"] = key
	}
	w := e.adminDo("POST", fmt.Sprintf("%s/agents/%d/adjust", adminBase, id), body, h)
	m := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func balanceOf(t *testing.T, id int64) int64 {
	t.Helper()
	a, err := db.GetAgent(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return a.BalanceCents
}

func TestAdminAdjustIdempotent(t *testing.T) {
	e := newAgentEnv(t)
	tier := e.seedTier("普通")
	id, _, _ := e.seedAgent("alice", tier)
	code, m := adjust(e, id, "k-1", `{"amount_cents":1000,"note":"首充"}`)
	if code != 200 || m["replayed"] != false || m["balance_cents"] != float64(1000) {
		t.Fatalf("首次: %d %v", code, m)
	}
	entry := m["entry"].(map[string]any)
	if entry["kind"] != "adjust" || entry["amount_cents"] != float64(1000) || entry["operator"] != "admin:admin" || entry["note"] != "首充" {
		t.Fatalf("流水行不对: %v", entry)
	}
	code, m = adjust(e, id, "k-1", `{"amount_cents":1000,"note":"首充"}`)
	if code != 200 || m["replayed"] != true || m["balance_cents"] != float64(1000) {
		t.Fatalf("重放: %d %v", code, m)
	}
	if b := balanceOf(t, id); b != 1000 {
		t.Fatalf("余额只应变一次: %d", b)
	}
	// 同键不同金额、不同代理商
	if code, m = adjust(e, id, "k-1", `{"amount_cents":2000,"note":"首充"}`); code != 409 || m["error"] != "idempotency_conflict" {
		t.Fatalf("同键别的金额应 409: %d %v", code, m)
	}
	id2, _, _ := e.seedAgent("bob", tier)
	if code, m = adjust(e, id2, "k-1", `{"amount_cents":1000,"note":"首充"}`); code != 409 || m["error"] != "idempotency_conflict" {
		t.Fatalf("同键别的代理商应 409: %d %v", code, m)
	}
	if b := balanceOf(t, id); b != 1000 {
		t.Fatalf("冲突不应改余额: %d", b)
	}
	var n int
	db.DB.QueryRow(`SELECT COUNT(*) FROM admin_audit_logs WHERE action = 'agent_adjust'`).Scan(&n)
	if n != 1 {
		t.Fatalf("重放不应再写审计: %d", n)
	}
}

func TestAdminAdjustRequiresKeyAndNote(t *testing.T) {
	e := newAgentEnv(t)
	tier := e.seedTier("普通")
	id, _, _ := e.seedAgent("alice", tier)
	if code, _ := adjust(e, id, "", `{"amount_cents":100,"note":"x"}`); code != 400 {
		t.Fatalf("缺键应 400: %d", code)
	}
	for _, body := range []string{`{"amount_cents":100,"note":""}`, `{"amount_cents":100,"note":"   "}`, `{"amount_cents":0,"note":"x"}`, `{"note":"x"}`, `{bad`} {
		code, m := adjust(e, id, "k-2", body)
		if code != 400 || m["error"] != "invalid_request" {
			t.Fatalf("%s 应 400 invalid_request: %d %v", body, code, m)
		}
	}
	if code, _ := adjust(e, 9999, "k-3", `{"amount_cents":100,"note":"x"}`); code != 404 {
		t.Fatalf("代理商不存在应 404: %d", code)
	}
	if b := balanceOf(t, id); b != 0 {
		t.Fatalf("参数错误不应改余额: %d", b)
	}
}

func TestAdminAdjustOverdraft(t *testing.T) {
	e := newAgentEnv(t)
	tier := e.seedTier("普通")
	id, _, _ := e.seedAgent("alice", tier)
	adjust(e, id, "k-1", `{"amount_cents":500,"note":"充"}`)
	code, m := adjust(e, id, "k-2", `{"amount_cents":-800,"note":"扣"}`)
	if code != 409 || m["error"] != "insufficient_balance" || m["balance_cents"] != float64(500) {
		t.Fatalf("透支应 409: %d %v", code, m)
	}
	if b := balanceOf(t, id); b != 500 {
		t.Fatalf("余额不应变: %d", b)
	}
	if code, _ = adjust(e, id, "k-3", `{"amount_cents":-500,"note":"扣"}`); code != 200 {
		t.Fatalf("扣到 0 应成功: %d", code)
	}
	// 只有成功的两笔（充 500、扣 500）留流水，被拒的那笔不留
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_ledger WHERE agent_id = ?`, id).Scan(&n); err != nil || n != 2 {
		t.Fatalf("流水应只有 2 条（被拒的透支不留流水）: %d %v", n, err)
	}
}

func TestAdminProductValidation(t *testing.T) {
	e := newAgentEnv(t)
	post := func(body string) *httpResp {
		return &httpResp{e.adminDo("POST", adminBase+"/agent-products", body, nil)}
	}
	ok := post(`{"product_code":"mw-plus-ph","name":"Plus 菲律宾","plan":"plus","region":"PH","sort":1}`)
	if ok.Code != 200 {
		t.Fatalf("建商品: %d %s", ok.Code, ok.Body.String())
	}
	p := decodeMap(t, ok.Body.String())["product"].(map[string]any)
	if p["product_code"] != "mw-plus-ph" || p["plan"] != "plus" || p["region"] != "PH" || p["enabled"] != true || p["sort"] != float64(1) {
		t.Fatalf("商品对象不对: %v", p)
	}
	id := int64(p["id"].(float64))
	bad := map[string]string{
		"套餐不支持": `{"product_code":"mw-x-ph","name":"x","plan":"nope","region":"PH","sort":0}`,
		"地区不支持": `{"product_code":"mw-plus-zz","name":"x","plan":"plus","region":"ZZ","sort":0}`,
		"编码太短":  `{"product_code":"a","name":"x","plan":"plus","region":"US","sort":0}`,
		"编码大写":  `{"product_code":"MW-PLUS-US","name":"x","plan":"plus","region":"US","sort":0}`,
		"名称为空":  `{"product_code":"mw-plus-us","name":"","plan":"plus","region":"US","sort":0}`,
	}
	for name, body := range bad {
		r := post(body)
		if r.Code != 400 || jsonField(t, r.Body.String(), "error") != "invalid_request" {
			t.Fatalf("%s 应 400: %d %s", name, r.Code, r.Body.String())
		}
	}
	for name, body := range map[string]string{
		"编码重复":   `{"product_code":"mw-plus-ph","name":"x","plan":"plus","region":"US","sort":0}`,
		"套餐地区重复": `{"product_code":"mw-other","name":"x","plan":"plus","region":"PH","sort":0}`,
	} {
		r := post(body)
		if r.Code != 409 || jsonField(t, r.Body.String(), "error") != "product_code_taken" {
			t.Fatalf("%s 应 409: %d %s", name, r.Code, r.Body.String())
		}
	}
	// PUT 改不了编码，只改名称、上下架、排序
	w := e.adminDo("PUT", fmt.Sprintf("%s/agent-products/%d", adminBase, id), `{"product_code":"hacked","plan":"pro_5x","region":"US","name":"新名","enabled":false,"sort":7}`, nil)
	if w.Code != 200 {
		t.Fatalf("改商品: %d %s", w.Code, w.Body.String())
	}
	p = decodeMap(t, w.Body.String())["product"].(map[string]any)
	if p["product_code"] != "mw-plus-ph" || p["plan"] != "plus" || p["region"] != "PH" || p["name"] != "新名" || p["enabled"] != false || p["sort"] != float64(7) {
		t.Fatalf("PUT 后不对: %v", p)
	}
	if w = e.adminDo("PUT", adminBase+"/agent-products/9999", `{"name":"x","enabled":true,"sort":0}`, nil); w.Code != 404 {
		t.Fatalf("不存在应 404: %d", w.Code)
	}
	if w = e.adminDo("PUT", fmt.Sprintf("%s/agent-products/%d", adminBase, id), `{"name":"","enabled":true,"sort":0}`, nil); w.Code != 400 {
		t.Fatalf("名称为空应 400: %d", w.Code)
	}
	if w = e.adminDo("PUT", fmt.Sprintf("%s/agent-products/%d", adminBase, id), `{"name":"x","sort":0}`, nil); w.Code != 400 {
		t.Fatalf("缺 enabled 应 400: %d", w.Code)
	}
	w = e.adminDo("GET", adminBase+"/agent-products", "", nil)
	if w.Code != 200 || len(decodeMap(t, w.Body.String())["items"].([]any)) != 1 {
		t.Fatalf("列表: %d %s", w.Code, w.Body.String())
	}
}

type httpResp struct{ *httptest.ResponseRecorder }

func TestAdminProductOptions(t *testing.T) {
	e := newAgentEnv(t)
	find := func() map[string]any {
		w := e.adminDo("GET", adminBase+"/agent-products/options", "", nil)
		if w.Code != 200 {
			t.Fatalf("options: %d %s", w.Code, w.Body.String())
		}
		for _, it := range decodeMap(t, w.Body.String())["items"].([]any) {
			m := it.(map[string]any)
			if m["plan"] == "plus" && m["region"] == "PH" {
				return m
			}
		}
		t.Fatal("缺 plus/PH")
		return nil
	}
	m := find()
	if m["default_code"] != "mw-plus-ph" || m["exists"] != false {
		t.Fatalf("建前: %v", m)
	}
	if w := e.adminDo("POST", adminBase+"/agent-products", `{"product_code":"mw-plus-ph","name":"x","plan":"plus","region":"PH","sort":0}`, nil); w.Code != 200 {
		t.Fatalf("建商品: %d", w.Code)
	}
	if m = find(); m["exists"] != true {
		t.Fatalf("建后 exists 应为 true: %v", m)
	}
}

func TestAdminPriceMatrixAndOverrides(t *testing.T) {
	e := newAgentEnv(t)
	t1, t2 := e.seedTier("普通"), e.seedTier("高级")
	aid, _, _ := e.seedAgent("alice", t1)
	var pids []int64
	for i, r := range []string{"PH", "US"} {
		w := e.adminDo("POST", adminBase+"/agent-products", fmt.Sprintf(`{"product_code":"mw-plus-%s","name":"P%d","plan":"plus","region":%q,"sort":%d}`, strings.ToLower(r), i, r, i), nil)
		if w.Code != 200 {
			t.Fatalf("建商品: %d %s", w.Code, w.Body.String())
		}
		pids = append(pids, int64(decodeMap(t, w.Body.String())["product"].(map[string]any)["id"].(float64)))
	}
	put := func(path, body string) (int, map[string]any) {
		w := e.adminDo("PUT", adminBase+path, body, nil)
		return w.Code, decodeMap(t, w.Body.String())
	}
	code, m := put("/agent-prices", fmt.Sprintf(`{"cells":[{"tier_id":%d,"product_id":%d,"price_cents":500},{"tier_id":%d,"product_id":%d,"price_cents":450},{"tier_id":%d,"product_id":%d,"price_cents":700}]}`, t1, pids[0], t2, pids[0], t1, pids[1]))
	if code != 200 || len(m["cells"].([]any)) != 3 || len(m["tiers"].([]any)) != 2 || len(m["products"].([]any)) != 2 {
		t.Fatalf("矩阵写入: %d %v", code, m)
	}
	// 0 = 不卖；负数整批拒绝
	code, m = put("/agent-prices", fmt.Sprintf(`{"cells":[{"tier_id":%d,"product_id":%d,"price_cents":0}]}`, t1, pids[1]))
	if code != 200 || len(m["cells"].([]any)) != 2 {
		t.Fatalf("0 应删除: %d %v", code, m)
	}
	code, m = put("/agent-prices", fmt.Sprintf(`{"cells":[{"tier_id":%d,"product_id":%d,"price_cents":999},{"tier_id":%d,"product_id":%d,"price_cents":-1}]}`, t1, pids[0], t2, pids[0]))
	if code != 400 || m["error"] != "invalid_request" {
		t.Fatalf("负价应 400: %d %v", code, m)
	}
	if p, _, _ := db.AgentPriceFor(context.Background(), aid, pids[0]); p != 500 {
		t.Fatalf("负价整批不应生效: %d", p)
	}
	// 同批里前面那格合法的新格（t1/PH 之前被 0 删掉了）也不能写进去
	if code, m = put("/agent-prices", fmt.Sprintf(`{"cells":[{"tier_id":%d,"product_id":%d,"price_cents":888},{"tier_id":%d,"product_id":%d,"price_cents":-1}]}`, t1, pids[1], t2, pids[0])); code != 400 {
		t.Fatalf("半批非法应 400: %d %v", code, m)
	}
	var nCell int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_tier_prices WHERE tier_id = ? AND product_id = ?`, t1, pids[1]).Scan(&nCell); err != nil || nCell != 0 {
		t.Fatalf("半批非法回滚后，前面的合法格不应写入: %d %v", nCell, err)
	}
	if code, m = put("/agent-prices", fmt.Sprintf(`{"cells":[{"tier_id":9999,"product_id":%d,"price_cents":1}]}`, pids[0])); code != 400 {
		t.Fatalf("等级不存在应 400: %d %v", code, m)
	}
	if code, m = put("/agent-prices", fmt.Sprintf(`{"cells":[{"tier_id":%d,"product_id":9999,"price_cents":1}]}`, t1)); code != 400 {
		t.Fatalf("商品不存在应 400: %d %v", code, m)
	}
	w := e.adminDo("GET", adminBase+"/agent-prices", "", nil)
	if w.Code != 200 || len(decodeMap(t, w.Body.String())["cells"].([]any)) != 2 {
		t.Fatalf("GET 矩阵: %d %s", w.Code, w.Body.String())
	}

	// 覆盖价
	ov := fmt.Sprintf("/agents/%d/overrides", aid)
	w = e.adminDo("GET", adminBase+ov, "", nil)
	items := decodeMap(t, w.Body.String())["items"].([]any)
	if w.Code != 200 || len(items) != 2 {
		t.Fatalf("GET 覆盖: %d %s", w.Code, w.Body.String())
	}
	first := items[0].(map[string]any)
	second := items[1].(map[string]any)
	if first["tier_price_cents"] != float64(500) || first["override_price_cents"] != nil || second["tier_price_cents"] != nil || first["product_code"] != "mw-plus-ph" {
		t.Fatalf("覆盖行不对: %v %v", first, second)
	}
	code, m = put(ov, fmt.Sprintf(`{"cells":[{"product_id":%d,"price_cents":300},{"product_id":%d,"price_cents":600}]}`, pids[0], pids[1]))
	items = m["items"].([]any)
	if code != 200 || items[0].(map[string]any)["override_price_cents"] != float64(300) || items[1].(map[string]any)["override_price_cents"] != float64(600) {
		t.Fatalf("写覆盖: %d %v", code, m)
	}
	if p, _, _ := db.AgentPriceFor(context.Background(), aid, pids[0]); p != 300 {
		t.Fatalf("覆盖价应生效: %d", p)
	}
	code, m = put(ov, fmt.Sprintf(`{"cells":[{"product_id":%d,"price_cents":0}]}`, pids[0]))
	if code != 200 || m["items"].([]any)[0].(map[string]any)["override_price_cents"] != nil {
		t.Fatalf("0 应取消覆盖: %d %v", code, m)
	}
	if code, _ = put(ov, fmt.Sprintf(`{"cells":[{"product_id":%d,"price_cents":-5}]}`, pids[0])); code != 400 {
		t.Fatalf("负价应 400: %d", code)
	}
	if code, _ = put(ov, `{"cells":[{"product_id":9999,"price_cents":5}]}`); code != 400 {
		t.Fatalf("商品不存在应 400: %d", code)
	}
	if code, m = put("/agents/9999/overrides", fmt.Sprintf(`{"cells":[{"product_id":%d,"price_cents":5}]}`, pids[0])); code != 404 || m["error"] != "not_found" {
		t.Fatalf("代理商不存在应 404: %d %v", code, m)
	}
	var n int
	db.DB.QueryRow(`SELECT COUNT(*) FROM agent_price_overrides WHERE agent_id = 9999`).Scan(&n)
	if n != 0 {
		t.Fatal("不存在的代理商不应留下覆盖价")
	}
	for _, act := range []string{"agent_price_update", "agent_override_update"} {
		db.DB.QueryRow(`SELECT COUNT(*) FROM admin_audit_logs WHERE action = ?`, act).Scan(&n)
		if n == 0 {
			t.Fatalf("缺审计 %s", act)
		}
	}
}

func TestAdminTiersCRUD(t *testing.T) {
	e := newAgentEnv(t)
	w := e.adminDo("POST", adminBase+"/agent-tiers", `{"name":"普通","note":"n"}`, nil)
	if w.Code != 200 {
		t.Fatalf("建等级: %d %s", w.Code, w.Body.String())
	}
	tier := decodeMap(t, w.Body.String())["tier"].(map[string]any)
	id := int64(tier["id"].(float64))
	if tier["name"] != "普通" || tier["note"] != "n" || tier["agent_count"] != float64(0) {
		t.Fatalf("等级对象: %v", tier)
	}
	if w = e.adminDo("POST", adminBase+"/agent-tiers", `{"name":"普通","note":""}`, nil); w.Code != 409 || jsonField(t, w.Body.String(), "error") != "name_taken" {
		t.Fatalf("重名应 409: %d %s", w.Code, w.Body.String())
	}
	if w = e.adminDo("POST", adminBase+"/agent-tiers", `{"name":"  ","note":""}`, nil); w.Code != 400 {
		t.Fatalf("空名应 400: %d", w.Code)
	}
	w = e.adminDo("PUT", fmt.Sprintf("%s/agent-tiers/%d", adminBase, id), `{"name":"标准","note":"m"}`, nil)
	if w.Code != 200 || decodeMap(t, w.Body.String())["tier"].(map[string]any)["name"] != "标准" {
		t.Fatalf("改等级: %d %s", w.Code, w.Body.String())
	}
	if w = e.adminDo("PUT", adminBase+"/agent-tiers/9999", `{"name":"x","note":""}`, nil); w.Code != 404 {
		t.Fatalf("改不存在应 404: %d", w.Code)
	}
	if w = e.adminDo("DELETE", adminBase+"/agent-tiers/9999", "", nil); w.Code != 404 {
		t.Fatalf("删不存在应 404: %d", w.Code)
	}
	w = e.adminDo("GET", adminBase+"/agent-tiers", "", nil)
	if w.Code != 200 || len(decodeMap(t, w.Body.String())["items"].([]any)) != 1 {
		t.Fatalf("列表: %d %s", w.Code, w.Body.String())
	}
	if w = e.adminDo("DELETE", fmt.Sprintf("%s/agent-tiers/%d", adminBase, id), "", nil); w.Code != 200 {
		t.Fatalf("删空等级: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminDeleteTierInUse(t *testing.T) {
	e := newAgentEnv(t)
	tier := e.seedTier("普通")
	e.seedAgent("alice", tier)
	w := e.adminDo("DELETE", fmt.Sprintf("%s/agent-tiers/%d", adminBase, tier), "", nil)
	if w.Code != 409 || jsonField(t, w.Body.String(), "error") != "tier_in_use" {
		t.Fatalf("在用应 409 tier_in_use: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminAgentSummary(t *testing.T) {
	e := newAgentEnv(t)
	tier := e.seedTier("普通")
	id, _, _ := e.seedAgent("alice", tier)
	adjust(e, id, "k-1", `{"amount_cents":1200,"note":"充"}`)
	w := e.adminDo("GET", adminBase+"/agent-summary", "", nil)
	m := decodeMap(t, w.Body.String())
	if w.Code != 200 || m["total_balance_cents"] != float64(1200) || m["today_order_cents"] != float64(0) || m["month_order_cents"] != float64(0) || m["reconcile"] != nil {
		t.Fatalf("总览: %d %s", w.Code, w.Body.String())
	}
	if err := db.SetSetting("agent_reconcile_last", `{"at":1700000000,"ok":false,"mismatches":[{"agent_id":3,"diff_cents":-50}]}`); err != nil {
		t.Fatal(err)
	}
	w = e.adminDo("GET", adminBase+"/agent-summary", "", nil)
	rec, _ := decodeMap(t, w.Body.String())["reconcile"].(map[string]any)
	if rec == nil || rec["ok"] != false || rec["at"] != float64(1700000000) || len(rec["mismatches"].([]any)) != 1 {
		t.Fatalf("对账结果: %s", w.Body.String())
	}
	if err := db.SetSetting("agent_reconcile_last", `not json`); err != nil {
		t.Fatal(err)
	}
	w = e.adminDo("GET", adminBase+"/agent-summary", "", nil)
	if w.Code != 200 || decodeMap(t, w.Body.String())["reconcile"] != nil {
		t.Fatalf("坏数据应得 null: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminAgentOrdersEmpty(t *testing.T) {
	e := newAgentEnv(t)
	tier := e.seedTier("普通")
	id, _, _ := e.seedAgent("alice", tier)
	w := e.adminDo("GET", fmt.Sprintf("%s/agents/%d/orders", adminBase, id), "", nil)
	m := decodeMap(t, w.Body.String())
	if w.Code != 200 || m["total"] != float64(0) || m["items"] == nil {
		t.Fatalf("订单: %d %s", w.Code, w.Body.String())
	}
	if w = e.adminDo("GET", adminBase+"/agents/9999/orders", "", nil); w.Code != 404 {
		t.Fatalf("不存在应 404: %d", w.Code)
	}
}

func TestAdminLocalCDKListAgentFilter(t *testing.T) {
	e := newAgentEnv(t)
	e.r.GET(adminBase+"/cdks", AdminListLocalCDKs)
	tier := e.seedTier("普通")
	a, _, _ := e.seedAgent("alice", tier)
	b, _, _ := e.seedAgent("bob", tier)
	ctx := context.Background()
	mk := func(tag string, agentID int64) {
		_, err := db.InsertLocalCDKs(ctx, "batch-"+tag, []db.NewLocalCDK{{
			Provider: "marswallex", CodeHash: "h-" + tag, TokenHash: "t-" + tag, CodePrefix: "MW-" + tag,
			Product: "gpt", PlanKey: "plus", Region: "PH", IssuedBy: "test", AgentID: agentID,
		}}, e.clock)
		if err != nil {
			t.Fatal(err)
		}
	}
	mk("A1", a)
	mk("A2", a)
	mk("B1", b)
	mk("S1", 0)
	list := func(q string) []map[string]any {
		w := e.adminDo("GET", adminBase+"/cdks"+q, "", nil)
		if w.Code != 200 {
			t.Fatalf("列表 %s: %d %s", q, w.Code, w.Body.String())
		}
		var out []map[string]any
		for _, it := range decodeMap(t, w.Body.String())["list"].([]any) {
			out = append(out, it.(map[string]any))
		}
		return out
	}
	rows := list(fmt.Sprintf("?agent_id=%d", a))
	if len(rows) != 2 {
		t.Fatalf("按 A 筛应 2 张: %d", len(rows))
	}
	for _, r := range rows {
		if r["agent_username"] != "alice" || r["agent_id"] != float64(a) {
			t.Fatalf("行不对: %v", r)
		}
	}
	all := list("")
	if len(all) != 4 {
		t.Fatalf("不筛应 4 张: %d", len(all))
	}
	for _, r := range all {
		if r["code_prefix"] == "MW-S1" && (r["agent_id"] != float64(0) || r["agent_username"] != "") {
			t.Fatalf("后台码应 0 和空串: %v", r)
		}
	}
	if zero := list("?agent_id=0"); len(zero) != 1 || zero[0]["code_prefix"] != "MW-S1" {
		t.Fatalf("agent_id=0 应只返回后台自己发的 MW-S1: %v", zero)
	}
	if w := e.adminDo("GET", adminBase+"/cdks?agent_id=abc", "", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("agent_id 不是数字应 400: %d", w.Code)
	}
}
