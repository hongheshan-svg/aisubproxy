package handler

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tuzi/cdk-recharge-system/internal/agent"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 余额流水：代理商的 /ledger、/ledger/export 和管理员的 /agents/:id/ledger、/agents/:id/ledger/export 共用这里的查询和 CSV。

const (
	agentLedgerDefaultDays = 30
	agentLedgerMaxDays     = 366
	agentLedgerCSVPage     = 1000
)

// ledgerRowView 对应契约里的 ledgerRow（代理商看到的流水不含操作人和外部单号）。
func ledgerRowView(e db.LedgerEntry) gin.H {
	v := adminLedgerRowView(e)
	delete(v, "operator")
	delete(v, "ext_ref")
	return v
}

func ledgerSummaryView(s db.LedgerSummary) gin.H {
	return gin.H{"opening_cents": s.OpeningCents, "closing_cents": s.ClosingCents, "income_cents": s.InCents, "expense_cents": s.OutCents}
}

// agentLedgerRange 读 from、to（Unix 秒，from <= created_at < to）。默认最近 30 天；
// 不是整数或 from > to 返回 400 invalid_request，跨度超过 366 天返回 400 range_too_large。出错时已写好响应。
func agentLedgerRange(c *gin.Context) (from, to int64, ok bool) {
	parse := func(key string) (int64, bool, bool) {
		s := strings.TrimSpace(c.Query(key))
		if s == "" {
			return 0, false, true
		}
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			agentOrderInvalid(c, "时间需为 Unix 秒整数")
			return 0, false, false
		}
		return v, true, true
	}
	f, hasFrom, ok1 := parse("from")
	t, hasTo, ok2 := parse("to")
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	day := int64(24 * time.Hour / time.Second)
	if !hasTo {
		t = agentNow().Unix() + 1 // 区间右开，加 1 秒才含此刻
	}
	if !hasFrom {
		f = t - agentLedgerDefaultDays*day
	}
	switch {
	case f < 0 || t < 0:
		agentOrderInvalid(c, "时间不能为负数")
		return 0, 0, false
	case f > t:
		agentOrderInvalid(c, "开始时间不能晚于结束时间")
		return 0, 0, false
	case t-f > agentLedgerMaxDays*day: // 两端都 >= 0，相减不会溢出
		agentError(c, http.StatusBadRequest, "range_too_large", fmt.Sprintf("查询范围最多 %d 天", agentLedgerMaxDays))
		return 0, 0, false
	}
	return f, t, true
}

// writeLedgerList 响应 {items, total, summary}。admin 为 true 时每行带操作人和外部单号。
func writeLedgerList(c *gin.Context, aid int64, admin bool, fail func(*gin.Context, string, error)) {
	from, to, ok := agentLedgerRange(c)
	if !ok {
		return
	}
	limit, offset := agentPaging(c)
	list, total, err := db.ListAgentLedger(c.Request.Context(), aid, from, to, limit, offset)
	if err != nil {
		fail(c, "列出流水", err)
		return
	}
	sum, err := db.AgentLedgerSummary(c.Request.Context(), aid, from, to)
	if err != nil {
		fail(c, "统计流水", err)
		return
	}
	items := make([]gin.H, 0, len(list))
	for _, e := range list {
		if admin {
			items = append(items, adminLedgerRowView(e))
		} else {
			items = append(items, ledgerRowView(e))
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "summary": ledgerSummaryView(sum)})
}

var ledgerKindLabel = map[string]string{db.LedgerPurchase: "下单", db.LedgerAdjust: "调账", db.LedgerTopup: "充值"}

// csvSafe 防 CSV 公式注入：用户输入的文本以 = + - @ 制表符 回车开头时，前面加一个单引号。
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// writeLedgerCSV 导出范围内的全部流水（按时间从新到旧）为带 BOM 的 CSV。admin 为 true 时多一列「操作人」。
func writeLedgerCSV(c *gin.Context, aid int64, username string, admin bool, fail func(*gin.Context, string, error)) {
	from, to, ok := agentLedgerRange(c)
	if !ok {
		return
	}
	if now := agentNow().Unix(); to > now { // 截到当前秒：导出期间新插入的行不会出现在翻页里造成重复或漏行
		to = now
	}
	var buf bytes.Buffer
	buf.Write([]byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM，让 Excel 认出中文
	w := csv.NewWriter(&buf)
	head := []string{"时间", "类型", "金额", "变动后余额", "订单号", "备注"}
	if admin {
		head = append(head, "操作人")
	}
	_ = w.Write(head)
	for offset := 0; ; offset += agentLedgerCSVPage {
		list, _, err := db.ListAgentLedger(c.Request.Context(), aid, from, to, agentLedgerCSVPage, offset)
		if err != nil {
			fail(c, "导出流水", err)
			return
		}
		for _, e := range list {
			kind := ledgerKindLabel[e.Kind]
			if kind == "" {
				kind = e.Kind
			}
			order := ""
			if e.OrderID != nil {
				order = strconv.FormatInt(*e.OrderID, 10)
			}
			rec := []string{
				time.Unix(e.CreatedAt, 0).In(beijingZone).Format("2006-01-02 15:04:05"), kind,
				agent.FormatCents(e.AmountCents), agent.FormatCents(e.BalanceAfterCents), csvSafe(order), csvSafe(e.Note),
			}
			if admin {
				rec = append(rec, csvSafe(e.Operator))
			}
			_ = w.Write(rec)
		}
		if len(list) < agentLedgerCSVPage {
			break
		}
	}
	w.Flush()
	name := fmt.Sprintf("ledger-%s-%s.csv", username, agentNow().In(beijingZone).Format("2006-01-02"))
	c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/csv; charset=utf-8", buf.Bytes())
}

// AgentLedger GET /ledger
func AgentLedger(c *gin.Context) {
	writeLedgerList(c, agentID(c), false, agentOrderBusy)
}

// AgentLedgerExport GET /ledger/export
func AgentLedgerExport(c *gin.Context) {
	writeLedgerCSV(c, agentID(c), agentUsername(c), false, agentOrderBusy)
}

// AdminAgentLedger GET /agents/:id/ledger
func AdminAgentLedger(c *gin.Context) {
	a, ok := adminAgentFromPath(c)
	if !ok {
		return
	}
	writeLedgerList(c, a.ID, true, func(c *gin.Context, op string, err error) { adminInternal(c, op, err) })
}

// AdminAgentLedgerExport GET /agents/:id/ledger/export
func AdminAgentLedgerExport(c *gin.Context) {
	a, ok := adminAgentFromPath(c)
	if !ok {
		return
	}
	writeLedgerCSV(c, a.ID, a.Username, true, func(c *gin.Context, op string, err error) { adminInternal(c, op, err) })
}
