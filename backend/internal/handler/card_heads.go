package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

// cardHeadSource 是可选能力：能从服务商读取卡头（卡产品）。没实现的服务商在后台手动维护卡头。
type cardHeadSource interface {
	CardHeads(ctx context.Context) ([]db.CardHeadProduct, error)
}

// cardHeadSourceFor 返回服务商的卡头数据源；供卡型、上游型的适配器都看一遍。
func cardHeadSourceFor(n provider.Name) (cardHeadSource, bool) {
	if a, ok := cardSupplyAdapterFor(n); ok {
		if s, ok := a.(cardHeadSource); ok {
			return s, true
		}
	}
	if a, ok := upstreamAdapterFor(n); ok {
		if s, ok := a.(cardHeadSource); ok {
			return s, true
		}
	}
	return nil, false
}

// 卡头取产品名里第一段 6 到 9 位的连续数字，两侧不能是数字。
var cardHeadInName = regexp.MustCompile(`(?:^|[^0-9])([0-9]{6,9})(?:[^0-9]|$)`)

// parseCardHead 从产品名里识别卡头，识别不出时返回空串。
func parseCardHead(name string) string {
	if m := cardHeadInName.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

const (
	cardHeadSyncDelay   = time.Minute
	cardHeadSyncEvery   = 30 * time.Minute
	cardHeadSyncTimeout = time.Minute
)

type cardHeadSyncStatus struct {
	at  int64
	err string
}

// 每家服务商最近一次同步的时间和错误，只记在内存里。
var (
	cardHeadSyncMu     sync.Mutex
	cardHeadSyncByName = map[provider.Name]cardHeadSyncStatus{}
)

// cardHeadSyncState 返回最近一次同步的时间（Unix 秒，没同步过为 0）和错误信息（成功为空）。
func cardHeadSyncState(n provider.Name) (lastAt int64, lastErr string) {
	cardHeadSyncMu.Lock()
	defer cardHeadSyncMu.Unlock()
	s := cardHeadSyncByName[n]
	return s.at, s.err
}

func setCardHeadSyncState(n provider.Name, err error) {
	s := cardHeadSyncStatus{at: time.Now().Unix()}
	if err != nil {
		s.err = err.Error()
	}
	cardHeadSyncMu.Lock()
	cardHeadSyncByName[n] = s
	cardHeadSyncMu.Unlock()
}

// cardHeadPruneMissing 是可选能力：接口不再返回的卡头直接删除，不保留。
// ZovoCard 用它：卡台下架的卡段 BIN 只能由卡台管理员看到，本站不保留（见 db.PruneCardProductsExcept）。
type cardHeadPruneMissing interface {
	PruneMissingCardHeads() bool
}

var errNoCardHeads = errors.New("服务商没有返回任何卡头")

// syncCardHeads 从数据源读取卡头并写入表里。产品 ID 不合法的条目跳过；
// 出错或没有合法条目时不改表，只记下错误。
func syncCardHeads(ctx context.Context, n provider.Name, src cardHeadSource) error {
	items, err := src.CardHeads(ctx)
	if err == nil {
		valid := make([]db.CardHeadProduct, 0, len(items))
		for _, it := range items {
			if cardHeadProductIDRe.MatchString(it.ProductID) {
				valid = append(valid, it)
			}
		}
		if skipped := len(items) - len(valid); skipped > 0 {
			log.Printf("同步 %s 卡头：跳过 %d 条产品 ID 不合法的记录", n, skipped)
		}
		items = valid
		if len(items) == 0 {
			err = errNoCardHeads
		}
	}
	if err == nil {
		prune := false
		if p, ok := src.(cardHeadPruneMissing); ok {
			prune = p.PruneMissingCardHeads()
		}
		err = db.SyncCardHeads(string(n), items, time.Now().Unix(), prune)
	}
	setCardHeadSyncState(n, err)
	return err
}

// syncAllCardHeads 同步所有能自动读取卡头的服务商。
func syncAllCardHeads(ctx context.Context) {
	for _, p := range provider.List() {
		src, ok := cardHeadSourceFor(p.Name())
		if !ok {
			continue
		}
		c, cancel := context.WithTimeout(ctx, cardHeadSyncTimeout)
		if err := syncCardHeads(c, p.Name(), src); err != nil && ctx.Err() == nil {
			log.Printf("同步 %s 卡头失败: %v", p.Name(), err)
		}
		cancel()
	}
}

// StartCardHeadSync 启动后台卡头同步：启动 1 分钟后一次，之后每 30 分钟一次。
// 每一轮单独兜住 panic：一轮出错，下一轮照常。
func StartCardHeadSync(ctx context.Context) {
	safego.Go("卡头同步循环", func() {
		safego.Loop(ctx, "卡头同步", cardHeadSyncDelay, cardHeadSyncEvery, syncAllCardHeads)
	})
}

// ---------- 后台接口 ----------

type cardHeadListResponse struct {
	Provider      string        `json:"provider"`
	Auto          bool          `json:"auto"`
	LastSyncAt    int64         `json:"last_sync_at"`
	LastSyncError string        `json:"last_sync_error"`
	Items         []db.CardHead `json:"items"`
}

var (
	cardHeadProductIDRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	cardHeadDigitsRe    = regexp.MustCompile(`^[0-9]{4,12}$`)
)

const (
	cardHeadMaxHeads   = 10
	cardHeadMaxName    = 100
	cardHeadMaxNote    = 200
	cardHeadMaxAttrLen = 16
)

// validateCardHead 校验手动录入的整条卡头：产品 ID、产品信息和标注。
func validateCardHead(h db.CardHead) error {
	if !cardHeadProductIDRe.MatchString(h.ProductID) {
		return errors.New("产品 ID 须为 1 到 64 位字母、数字、下划线、横线或点")
	}
	if err := validateCardHeadNotes(h); err != nil {
		return err
	}
	if utf8.RuneCountInString(h.Name) > cardHeadMaxName {
		return fmt.Errorf("名称最多 %d 字", cardHeadMaxName)
	}
	if len(h.CardHeads) > cardHeadMaxHeads {
		return fmt.Errorf("卡头最多 %d 个", cardHeadMaxHeads)
	}
	for _, d := range h.CardHeads {
		if !cardHeadDigitsRe.MatchString(d) {
			return fmt.Errorf("卡头 %q 须为 4 到 12 位数字", d)
		}
	}
	for label, v := range map[string]string{"卡组织": h.Network, "国家": h.Country, "币种": h.Currency} {
		if utf8.RuneCountInString(v) > cardHeadMaxAttrLen {
			return fmt.Errorf("%s最多 %d 字", label, cardHeadMaxAttrLen)
		}
	}
	return nil
}

// validateCardHeadNotes 只校验标注（3DS、Apple Pay、备注）。接口来源的行只校验这部分：
// 它的产品信息来自上游，不受手动录入的上限约束，否则超限的行就永远标不了。
func validateCardHeadNotes(h db.CardHead) error {
	if utf8.RuneCountInString(h.Note) > cardHeadMaxNote {
		return fmt.Errorf("备注最多 %d 字", cardHeadMaxNote)
	}
	for label, v := range map[string]string{"3DS": h.ThreeDS, "Apple Pay": h.ApplePay} {
		if v != "" && v != "yes" && v != "no" {
			return fmt.Errorf("%s 只能是未标注、支持或不支持", label)
		}
	}
	return nil
}

// cardHeadProvider 取地址里的服务商；未注册时写 404 并返回 false。
func cardHeadProvider(c *gin.Context) (provider.Name, bool) {
	p, ok := provider.Get(provider.Name(c.Param("name")))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "服务商不存在"})
		return "", false
	}
	return p.Name(), true
}

func writeCardHeadList(c *gin.Context, n provider.Name) {
	items, err := db.ListCardHeads(string(n))
	if err != nil {
		log.Printf("list card heads %s: %v", n, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取卡头失败"})
		return
	}
	_, auto := cardHeadSourceFor(n)
	at, lastErr := cardHeadSyncState(n)
	c.JSON(http.StatusOK, cardHeadListResponse{Provider: string(n), Auto: auto, LastSyncAt: at, LastSyncError: lastErr, Items: items})
}

func cardHeadDBError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, db.ErrCardHeadNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, db.ErrCardHeadNotManual):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		log.Printf("card heads: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存卡头失败"})
	}
}

// AdminListCardHeads GET /api/v1/admin/providers/:name/card-heads
func AdminListCardHeads(c *gin.Context) {
	if n, ok := cardHeadProvider(c); ok {
		writeCardHeadList(c, n)
	}
}

// AdminSyncCardHeads POST /api/v1/admin/providers/:name/card-heads/sync
func AdminSyncCardHeads(c *gin.Context) {
	n, ok := cardHeadProvider(c)
	if !ok {
		return
	}
	src, auto := cardHeadSourceFor(n)
	if !auto {
		c.JSON(http.StatusBadRequest, gin.H{"error": "这家服务商不能自动读取卡头，请手动添加"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), cardHeadSyncTimeout)
	defer cancel()
	if err := syncCardHeads(ctx, n, src); err != nil {
		auditAdmin(c, "card_heads_sync", fmt.Sprintf("provider=%s 失败", n))
		c.JSON(http.StatusBadGateway, gin.H{"error": "同步失败：" + err.Error()})
		return
	}
	auditAdmin(c, "card_heads_sync", fmt.Sprintf("provider=%s", n))
	writeCardHeadList(c, n)
}

type cardHeadBody struct {
	ProductID string   `json:"product_id"`
	Name      string   `json:"name"`
	CardHeads []string `json:"card_heads"`
	Network   string   `json:"network"`
	Country   string   `json:"country"`
	Currency  string   `json:"currency"`
	ThreeDS   string   `json:"three_ds"`
	ApplePay  string   `json:"apple_pay"`
	Note      string   `json:"note"`
}

// AdminAddCardHead POST /api/v1/admin/providers/:name/card-heads：手动添加，只对不能自动读取的服务商开放。
func AdminAddCardHead(c *gin.Context) {
	n, ok := cardHeadProvider(c)
	if !ok {
		return
	}
	if _, auto := cardHeadSourceFor(n); auto {
		c.JSON(http.StatusBadRequest, gin.H{"error": "这家服务商的卡头由接口自动读取，不能手动添加"})
		return
	}
	var b cardHeadBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数格式错误"})
		return
	}
	h := db.CardHead{Provider: string(n), ProductID: strings.TrimSpace(b.ProductID), Name: strings.TrimSpace(b.Name),
		CardHeads: trimAll(b.CardHeads), Network: strings.TrimSpace(b.Network), Country: strings.TrimSpace(b.Country),
		Currency: strings.TrimSpace(b.Currency), ThreeDS: b.ThreeDS, ApplePay: b.ApplePay, Note: strings.TrimSpace(b.Note)}
	if err := validateCardHead(h); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if _, err := db.GetCardHead(h.Provider, h.ProductID); err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "这个产品 ID 已经有了，请直接编辑"})
		return
	} else if !errors.Is(err, db.ErrCardHeadNotFound) {
		cardHeadDBError(c, err)
		return
	}
	if err := db.SaveManualCardHead(h, c.GetString("username"), time.Now().Unix()); err != nil {
		cardHeadDBError(c, err)
		return
	}
	auditAdmin(c, "card_head_add", fmt.Sprintf("provider=%s product_id=%s", n, h.ProductID))
	writeCardHeadList(c, n)
}

type cardHeadPatch struct {
	ThreeDS   *string   `json:"three_ds"`
	ApplePay  *string   `json:"apple_pay"`
	Note      *string   `json:"note"`
	Name      *string   `json:"name"`
	CardHeads *[]string `json:"card_heads"`
	Network   *string   `json:"network"`
	Country   *string   `json:"country"`
	Currency  *string   `json:"currency"`
}

// AdminUpdateCardHead PUT /api/v1/admin/providers/:name/card-heads/:product_id：
// 改标注；手动添加的还能改产品信息。没带的字段保持原值。
func AdminUpdateCardHead(c *gin.Context) {
	n, ok := cardHeadProvider(c)
	if !ok {
		return
	}
	var p cardHeadPatch
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数格式错误"})
		return
	}
	h, err := db.GetCardHead(string(n), c.Param("product_id"))
	if err != nil {
		cardHeadDBError(c, err)
		return
	}
	var fields []string
	set := func(name string, dst *string, v *string) {
		if v != nil && strings.TrimSpace(*v) != *dst {
			*dst = strings.TrimSpace(*v)
			fields = append(fields, name)
		}
	}
	set("three_ds", &h.ThreeDS, p.ThreeDS)
	set("apple_pay", &h.ApplePay, p.ApplePay)
	set("note", &h.Note, p.Note)
	manual := h.Source == db.CardHeadSourceManual
	if manual {
		set("name", &h.Name, p.Name)
		set("network", &h.Network, p.Network)
		set("country", &h.Country, p.Country)
		set("currency", &h.Currency, p.Currency)
		if p.CardHeads != nil && strings.Join(trimAll(*p.CardHeads), ",") != strings.Join(h.CardHeads, ",") {
			h.CardHeads = trimAll(*p.CardHeads)
			fields = append(fields, "card_heads")
		}
	}
	validate := validateCardHeadNotes
	if manual {
		validate = validateCardHead
	}
	if err := validate(h); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	by, now := c.GetString("username"), time.Now().Unix()
	if manual {
		err = db.SaveManualCardHead(h, by, now)
	} else {
		err = db.UpdateCardHeadNotes(h.Provider, h.ProductID, h.ThreeDS, h.ApplePay, h.Note, by, now)
	}
	if err != nil {
		cardHeadDBError(c, err)
		return
	}
	auditAdmin(c, "card_head_update", fmt.Sprintf("provider=%s product_id=%s fields=%s", n, h.ProductID, strings.Join(fields, ",")))
	writeCardHeadList(c, n)
}

// AdminDeleteCardHead DELETE /api/v1/admin/providers/:name/card-heads/:product_id：只能删手动添加的。
func AdminDeleteCardHead(c *gin.Context) {
	n, ok := cardHeadProvider(c)
	if !ok {
		return
	}
	id := c.Param("product_id")
	if err := db.DeleteManualCardHead(string(n), id); err != nil {
		cardHeadDBError(c, err)
		return
	}
	auditAdmin(c, "card_head_delete", fmt.Sprintf("provider=%s product_id=%s", n, id))
	writeCardHeadList(c, n)
}

func trimAll(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, strings.TrimSpace(s))
	}
	return out
}
