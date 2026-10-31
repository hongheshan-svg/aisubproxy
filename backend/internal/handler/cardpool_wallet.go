package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

// 账户流水不传时间时默认查最近 7 天
const defaultSerialsWindow = 7 * 24 * time.Hour

// ---------- 钱包账户 ----------

// AdminCardPoolAccounts 列出本地账户；refresh=1 时先同步，同步失败返回错误而不是旧数据。
func AdminCardPoolAccounts(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		ctx := c.Request.Context()
		if c.Query("refresh") == "1" {
			if err := s.SyncAccounts(ctx); err != nil {
				poolFail(c, err)
				return
			}
		}
		items, err := s.ListAccounts(ctx)
		poolList(c, items, len(items), err)
	})(c)
}

// AdminCardPoolCreateAccount 新建子账户；结果未知时原样返回平台错误，由前端稍后刷新。
func AdminCardPoolCreateAccount(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		var req struct {
			AccountName string `json:"account_name"`
		}
		if !bindPoolJSON(c, &req) {
			return
		}
		a, err := s.CreateSubAccount(c.Request.Context(), req.AccountName, adminName(c))
		if err != nil {
			poolFail(c, err)
			return
		}
		poolAudit(c, "wallet_create_account", fmt.Sprintf("account_no=%s name=%s", a.AccountNo, a.AccountName))
		c.JSON(http.StatusOK, a)
	})(c)
}

// AdminCardPoolAccountStatus 启用或停用子账户。
func AdminCardPoolAccountStatus(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		var req struct {
			Status string `json:"status"`
			Remark string `json:"remark"`
		}
		if !bindPoolJSON(c, &req) {
			return
		}
		no := c.Param("no")
		a, err := s.SetSubAccountStatus(c.Request.Context(), no, req.Status, req.Remark, adminName(c))
		if err != nil {
			poolFail(c, err)
			return
		}
		poolAudit(c, "wallet_account_status", fmt.Sprintf("account_no=%s status=%s remark=%s",
			a.AccountNo, req.Status, strings.TrimSpace(req.Remark)))
		c.JSON(http.StatusOK, a)
	})(c)
}

// AdminCardPoolReceivingAddresses 实时查询数币收款地址。
func AdminCardPoolReceivingAddresses(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		items, err := s.ReceivingAddresses(c.Request.Context())
		poolList(c, items, len(items), err)
	})(c)
}

// serialsWindow 解析账户流水的 start_time / end_time（Unix 秒）：都不传为最近 7 天，
// 只传开始则截止到现在，只传截止则往前 7 天。格式非法返回 false。
func serialsWindow(c *gin.Context) (int64, int64, bool) {
	parse := func(name string) (int64, bool, bool) {
		v := strings.TrimSpace(c.Query(name))
		if v == "" {
			return 0, false, true
		}
		n, err := strconv.ParseInt(v, 10, 64)
		return n, true, err == nil && n > 0
	}
	start, hasStart, ok1 := parse("start_time")
	end, hasEnd, ok2 := parse("end_time")
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	if !hasEnd {
		end = time.Now().Unix()
	}
	if !hasStart {
		start = end - int64(defaultSerialsWindow/time.Second)
	}
	return start, end, true
}

// AdminCardPoolSerials 查询账户流水（实时透传）。账户号、币种必填，窗口最长 90 天。
func AdminCardPoolSerials(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		start, end, ok := serialsWindow(c)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "start_time / end_time 须为 Unix 秒"})
			return
		}
		page, size := pageParams(c)
		items, total, err := s.AccountSerials(c.Request.Context(), cardpool.SerialsQuery{
			AccountNo: strings.TrimSpace(c.Query("account_no")), Currency: strings.TrimSpace(c.Query("currency")),
			Start: start, End: end, Page: page, PageSize: size,
		})
		poolList(c, items, total, err)
	})(c)
}

// ---------- 兑换与钱包转账 ----------

// AdminCardPoolConversion 发起一笔兑换（主钱包）。单据从不自动重发；已有在途兑换单时回 409。
func AdminCardPoolConversion(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		var req struct {
			SellCurrency string `json:"sell_currency"`
			SellAmount   string `json:"sell_amount"`
			BuyCurrency  string `json:"buy_currency"`
		}
		if !bindPoolJSON(c, &req) {
			return
		}
		cents, ok := parsePoolAmount(c, req.SellAmount)
		if !ok {
			return
		}
		o, err := s.SubmitConversion(c.Request.Context(), cardpool.ConversionParams{
			SellCurrency: req.SellCurrency, BuyCurrency: req.BuyCurrency, SellCents: cents, By: adminName(c),
		})
		if err != nil {
			poolFail(c, err)
			return
		}
		amt, _ := marswallex.FormatAmount(o.AmountCents)
		poolAudit(c, "wallet_conversion", fmt.Sprintf("out_order_no=%s sell=%s%s buy=%s", o.OutOrderNo, amt, o.Currency, o.BuyCurrency))
		c.JSON(http.StatusOK, o)
	})(c)
}

// AdminCardPoolWalletTransfer 发起一笔主钱包与子账户之间的转账。单据从不自动重发；已有在途转账单时回 409。
func AdminCardPoolWalletTransfer(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		var req struct {
			PayerAccountNo string `json:"payer_account_no"`
			PayeeAccountNo string `json:"payee_account_no"`
			Currency       string `json:"currency"`
			Amount         string `json:"amount"`
			Remark         string `json:"remark"`
		}
		if !bindPoolJSON(c, &req) {
			return
		}
		cents, ok := parsePoolAmount(c, req.Amount)
		if !ok {
			return
		}
		o, err := s.SubmitWalletTransfer(c.Request.Context(), cardpool.WalletTransferParams{
			PayerAccountNo: req.PayerAccountNo, PayeeAccountNo: req.PayeeAccountNo, Currency: req.Currency,
			Remark: req.Remark, AmountCents: cents, By: adminName(c),
		})
		if err != nil {
			poolFail(c, err)
			return
		}
		amt, _ := marswallex.FormatAmount(o.AmountCents)
		poolAudit(c, "wallet_transfer", fmt.Sprintf("out_order_no=%s payer=%s payee=%s amount=%s currency=%s",
			o.OutOrderNo, o.PayerAccountNo, o.PayeeAccountNo, amt, o.Currency))
		c.JSON(http.StatusOK, o)
	})(c)
}

// ---------- 交易补拉、卡转入转出明细 ----------

// AdminCardPoolBackfillStatus 返回交易补拉状态。
func AdminCardPoolBackfillStatus(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		st, err := s.BackfillStatus(c.Request.Context())
		if err != nil {
			poolFail(c, err)
			return
		}
		c.JSON(http.StatusOK, st)
	})(c)
}

// AdminCardPoolBackfill 手动补拉最近 1 / 3 / 7 天。在请求内同步执行（与定时补拉串行），返回补拉后的状态。
func AdminCardPoolBackfill(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		var req struct {
			Days int `json:"days"`
		}
		if !bindPoolJSON(c, &req) {
			return
		}
		st, err := s.BackfillRecent(c.Request.Context(), req.Days)
		if err != nil {
			poolFail(c, err)
			return
		}
		poolAudit(c, "backfill", fmt.Sprintf("days=%d", req.Days))
		c.JSON(http.StatusOK, st)
	})(c)
}

// AdminCardPoolCardTransfers 实时查询卡的转入转出明细，并标出与本地单据的比对结果。
func AdminCardPoolCardTransfers(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		items, err := s.CardTransfers(c.Request.Context(), c.Param("id"))
		poolList(c, items, len(items), err)
	})(c)
}
