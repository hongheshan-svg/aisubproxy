package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/tuzi/cdk-recharge-system/internal/agent"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// 等级、批发商品、价格矩阵。

const (
	agentPriceCellsMax  = 10000
	agentPriceMaxCents  = int64(100_000_000) // 单价上限 $1,000,000
	agentTierNameMax    = 50
	agentProductNameMax = 100
	agentProductProv    = "marswallex"
	agentProductLine    = "gpt"
)

// validPriceCents 价格不能为负（0 表示删除或不卖），也不能大到离谱。
func validPriceCents(c *gin.Context, v int64) bool {
	if v < 0 {
		adminInvalid(c, "价格不能为负数")
		return false
	}
	if v > agentPriceMaxCents {
		adminInvalid(c, "价格超出范围")
		return false
	}
	return true
}

// ---- 等级 ----

func agentTierView(t db.AgentTier) gin.H {
	return gin.H{"id": t.ID, "name": t.Name, "note": t.Note, "agent_count": t.AgentCount}
}

func adminTierResponse(c *gin.Context, id int64) {
	list, err := db.ListAgentTiers(c.Request.Context())
	if err != nil {
		adminInternal(c, "列出等级", err)
		return
	}
	for _, t := range list {
		if t.ID == id {
			c.JSON(http.StatusOK, gin.H{"tier": agentTierView(t)})
			return
		}
	}
	adminNotFound(c, "等级")
}

func adminTierBody(c *gin.Context) (name, note string, ok bool) {
	var req struct {
		Name string `json:"name"`
		Note string `json:"note"`
	}
	if !adminBind(c, &req) {
		return "", "", false
	}
	name, note = strings.TrimSpace(req.Name), strings.TrimSpace(req.Note)
	if name == "" || utf8.RuneCountInString(name) > agentTierNameMax {
		adminInvalid(c, fmt.Sprintf("等级名不能为空，且不超过 %d 个字", agentTierNameMax))
		return "", "", false
	}
	if utf8.RuneCountInString(note) > agentNoteMax {
		adminInvalid(c, fmt.Sprintf("备注不能超过 %d 个字", agentNoteMax))
		return "", "", false
	}
	return name, note, true
}

// AdminListAgentTiers GET /agent-tiers
func AdminListAgentTiers(c *gin.Context) {
	list, err := db.ListAgentTiers(c.Request.Context())
	if err != nil {
		adminInternal(c, "列出等级", err)
		return
	}
	items := make([]gin.H, 0, len(list))
	for _, t := range list {
		items = append(items, agentTierView(t))
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// AdminCreateAgentTier POST /agent-tiers
func AdminCreateAgentTier(c *gin.Context) {
	name, note, ok := adminTierBody(c)
	if !ok {
		return
	}
	id, err := db.CreateAgentTier(c.Request.Context(), name, note, time.Now())
	if errors.Is(err, db.ErrAgentTierNameTaken) {
		agentError(c, http.StatusConflict, "name_taken", "等级名已被使用")
		return
	}
	if err != nil {
		adminInternal(c, "建等级", err)
		return
	}
	auditAdmin(c, "agent_tier_create", fmt.Sprintf("id=%d name=%s", id, name))
	adminTierResponse(c, id)
}

// AdminUpdateAgentTier PUT /agent-tiers/:id
func AdminUpdateAgentTier(c *gin.Context) {
	id, ok := adminPathID(c, "等级")
	if !ok {
		return
	}
	name, note, ok := adminTierBody(c)
	if !ok {
		return
	}
	err := db.UpdateAgentTier(c.Request.Context(), id, name, note, time.Now())
	switch {
	case errors.Is(err, db.ErrAgentTierNameTaken):
		agentError(c, http.StatusConflict, "name_taken", "等级名已被使用")
		return
	case errors.Is(err, db.ErrAgentTierNotFound):
		adminNotFound(c, "等级")
		return
	case err != nil:
		adminInternal(c, "改等级", err)
		return
	}
	auditAdmin(c, "agent_tier_update", fmt.Sprintf("id=%d name=%s", id, name))
	adminTierResponse(c, id)
}

// AdminDeleteAgentTier DELETE /agent-tiers/:id
func AdminDeleteAgentTier(c *gin.Context) {
	id, ok := adminPathID(c, "等级")
	if !ok {
		return
	}
	err := db.DeleteAgentTier(c.Request.Context(), id)
	switch {
	case errors.Is(err, db.ErrAgentTierInUse):
		agentError(c, http.StatusConflict, "tier_in_use", "还有代理商在用这个等级，不能删除")
		return
	case errors.Is(err, db.ErrAgentTierNotFound):
		adminNotFound(c, "等级")
		return
	case err != nil:
		adminInternal(c, "删等级", err)
		return
	}
	auditAdmin(c, "agent_tier_delete", fmt.Sprintf("id=%d", id))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- 商品 ----

func agentProductView(p db.AgentProduct) gin.H {
	return gin.H{"id": p.ID, "product_code": p.Code, "name": p.Name, "plan": p.PlanKey, "region": p.Region, "enabled": p.Enabled, "sort": p.Sort}
}

func adminProductResponse(c *gin.Context, id int64) {
	list, err := db.ListAgentProducts(c.Request.Context())
	if err != nil {
		adminInternal(c, "列出商品", err)
		return
	}
	for _, p := range list {
		if p.ID == id {
			c.JSON(http.StatusOK, gin.H{"product": agentProductView(p)})
			return
		}
	}
	adminNotFound(c, "商品")
}

// AdminListAgentProducts GET /agent-products
func AdminListAgentProducts(c *gin.Context) {
	list, err := db.ListAgentProducts(c.Request.Context())
	if err != nil {
		adminInternal(c, "列出商品", err)
		return
	}
	items := make([]gin.H, 0, len(list))
	for _, p := range list {
		items = append(items, agentProductView(p))
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// AdminAgentProductOptions GET /agent-products/options：MarsWallex 已支持的「套餐 × 地区」，exists 表示已建过商品。
func AdminAgentProductOptions(c *gin.Context) {
	list, err := db.ListAgentProducts(c.Request.Context())
	if err != nil {
		adminInternal(c, "列出商品", err)
		return
	}
	have := map[string]bool{}
	for _, p := range list {
		if p.Provider == agentProductProv && p.Product == agentProductLine {
			have[p.PlanKey+"/"+p.Region] = true
		}
	}
	items := []gin.H{}
	for _, plan := range payengine.SupportedPlanKeys() {
		if !provider.Supports(provider.MarsWallex, agentProductLine, plan) {
			continue
		}
		for _, region := range payengine.Regions {
			items = append(items, gin.H{"plan": plan, "region": region, "default_code": agent.DefaultProductCode(plan, region), "exists": have[plan+"/"+region]})
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func validProductName(c *gin.Context, name string) bool {
	if name == "" || utf8.RuneCountInString(name) > agentProductNameMax {
		adminInvalid(c, fmt.Sprintf("商品名称不能为空，且不超过 %d 个字", agentProductNameMax))
		return false
	}
	return true
}

// AdminCreateAgentProduct POST /agent-products：服务商固定 marswallex、产品线固定 gpt。
func AdminCreateAgentProduct(c *gin.Context) {
	var req struct {
		ProductCode string `json:"product_code"`
		Name        string `json:"name"`
		Plan        string `json:"plan"`
		Region      string `json:"region"`
		Sort        int    `json:"sort"`
	}
	if !adminBind(c, &req) {
		return
	}
	code, plan := strings.TrimSpace(req.ProductCode), strings.TrimSpace(req.Plan)
	region, name := strings.ToUpper(strings.TrimSpace(req.Region)), strings.TrimSpace(req.Name)
	if !agent.ValidProductCode(code) {
		adminInvalid(c, "商品编码需为 2–48 位小写字母、数字或短横线")
		return
	}
	if !provider.Supports(provider.MarsWallex, agentProductLine, plan) {
		adminInvalid(c, "MarsWallex 不支持这个套餐")
		return
	}
	if !containsString(payengine.Regions, region) {
		adminInvalid(c, "不支持这个地区")
		return
	}
	if !validProductName(c, name) {
		return
	}
	id, err := db.CreateAgentProduct(c.Request.Context(), db.AgentProduct{
		Code: code, Provider: agentProductProv, Product: agentProductLine, PlanKey: plan, Region: region,
		Name: name, Enabled: true, Sort: req.Sort,
	}, time.Now())
	if errors.Is(err, db.ErrAgentProductExists) {
		agentError(c, http.StatusConflict, "product_code_taken", "商品编码或该套餐×地区已存在")
		return
	}
	if err != nil {
		adminInternal(c, "建商品", err)
		return
	}
	auditAdmin(c, "agent_product_create", fmt.Sprintf("id=%d code=%s plan=%s region=%s", id, code, plan, region))
	adminProductResponse(c, id)
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// AdminUpdateAgentProduct PUT /agent-products/:id：只改名称、上下架、排序，编码等字段忽略。
func AdminUpdateAgentProduct(c *gin.Context) {
	id, ok := adminPathID(c, "商品")
	if !ok {
		return
	}
	var req struct {
		Name    string `json:"name"`
		Enabled *bool  `json:"enabled"`
		Sort    int    `json:"sort"`
	}
	if !adminBind(c, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if !validProductName(c, name) {
		return
	}
	if req.Enabled == nil {
		adminInvalid(c, "缺少 enabled")
		return
	}
	err := db.UpdateAgentProduct(c.Request.Context(), id, name, *req.Enabled, req.Sort, time.Now())
	if errors.Is(err, db.ErrAgentProductNotFound) {
		adminNotFound(c, "商品")
		return
	}
	if err != nil {
		adminInternal(c, "改商品", err)
		return
	}
	auditAdmin(c, "agent_product_update", fmt.Sprintf("id=%d enabled=%t sort=%d", id, *req.Enabled, req.Sort))
	adminProductResponse(c, id)
}

// ---- 价格矩阵 ----

func adminPriceMatrixResponse(c *gin.Context) {
	ctx := c.Request.Context()
	tiers, err := db.ListAgentTiers(ctx)
	if err != nil {
		adminInternal(c, "列出等级", err)
		return
	}
	products, err := db.ListAgentProducts(ctx)
	if err != nil {
		adminInternal(c, "列出商品", err)
		return
	}
	cells, err := db.ListAgentTierPrices(ctx)
	if err != nil {
		adminInternal(c, "列出价格", err)
		return
	}
	ts := make([]gin.H, 0, len(tiers))
	for _, t := range tiers {
		ts = append(ts, gin.H{"id": t.ID, "name": t.Name})
	}
	ps := make([]gin.H, 0, len(products))
	for _, p := range products {
		ps = append(ps, gin.H{"id": p.ID, "product_code": p.Code, "name": p.Name, "enabled": p.Enabled})
	}
	cs := make([]gin.H, 0, len(cells))
	for _, x := range cells {
		cs = append(cs, gin.H{"tier_id": x.TierID, "product_id": x.ProductID, "price_cents": x.PriceCents})
	}
	c.JSON(http.StatusOK, gin.H{"tiers": ts, "products": ps, "cells": cs})
}

// AdminGetAgentPrices GET /agent-prices
func AdminGetAgentPrices(c *gin.Context) { adminPriceMatrixResponse(c) }

// AdminPutAgentPrices PUT /agent-prices：大于 0 写入，等于 0 表示不卖，负数 400；整批一个事务。
func AdminPutAgentPrices(c *gin.Context) {
	var req struct {
		Cells []struct {
			TierID     int64 `json:"tier_id"`
			ProductID  int64 `json:"product_id"`
			PriceCents int64 `json:"price_cents"`
		} `json:"cells"`
	}
	if !adminBind(c, &req) {
		return
	}
	if len(req.Cells) > agentPriceCellsMax {
		adminInvalid(c, "一次提交的价格太多")
		return
	}
	cells := make([]db.AgentPriceCell, 0, len(req.Cells))
	for _, x := range req.Cells {
		if x.TierID <= 0 || x.ProductID <= 0 {
			adminInvalid(c, "等级或商品 ID 不合法")
			return
		}
		if !validPriceCents(c, x.PriceCents) {
			return
		}
		cells = append(cells, db.AgentPriceCell{TierID: x.TierID, ProductID: x.ProductID, PriceCents: x.PriceCents})
	}
	err := db.PutAgentTierPrices(c.Request.Context(), cells, time.Now())
	switch {
	case errors.Is(err, db.ErrAgentTierNotFound):
		adminInvalid(c, "等级不存在")
		return
	case errors.Is(err, db.ErrAgentProductNotFound):
		adminInvalid(c, "商品不存在")
		return
	case err != nil:
		adminInternal(c, "写价格", err)
		return
	}
	auditAdmin(c, "agent_price_update", fmt.Sprintf("cells=%d", len(cells)))
	adminPriceMatrixResponse(c)
}
