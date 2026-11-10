package handler

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// 「卡服务商」页的后台接口。界面按这里给的数据渲染，不按服务商名写死。

// providerJSON 是服务商列表里的一家。code_prefix 只有供卡型有。
func providerJSON(p provider.Provider) gin.H {
	products := p.Products()
	if products == nil {
		products = []string{}
	}
	out := gin.H{
		"name":         p.Name(),
		"display_name": p.DisplayName(),
		"kind":         p.Kind(),
		"kind_text":    p.Kind().Text(),
		"products":     products,
		"code_list":    provider.CodeList(p),
	}
	if cs, ok := provider.AsCardSupply(p); ok {
		out["code_prefix"] = cs.CodePrefix()
	}
	return out
}

// providersOverview 是 GET /admin/providers 的内容，PUT /admin/providers/config 成功时也返回它。
func providersOverview(cfg provider.Config) gin.H {
	list := []gin.H{}
	for _, p := range provider.List() {
		list = append(list, providerJSON(p))
	}
	by := cfg.ByProduct
	if by == nil {
		by = map[string]provider.Name{}
	}
	return gin.H{
		"providers":    list,
		"products":     provider.Products(),
		"default":      cfg.Default,
		"by_product":   by,
		"effective":    cfg.Effective(),
		"warnings":     nonNilStrings(cfg.Warnings),
		"capabilities": provider.Capabilities(),
	}
}

// AdminListProviders GET /api/v1/admin/providers
// 已注册的服务商、产品线、当前配置（全局默认、按产品、实际生效）、配置里的问题、能力矩阵。
func AdminListProviders(c *gin.Context) {
	cfg, err := provider.LoadConfig()
	if err != nil {
		log.Printf("[provider] 读取服务商配置失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取服务商配置失败"})
		return
	}
	c.JSON(http.StatusOK, providersOverview(cfg))
}

// AdminPutProviderConfig PUT /api/v1/admin/providers/config  body: {"default": "marswallex", "by_product": {"gpt": "marswallex", "x": ""}}
// default 必填。by_product 给了就整份替换，值为空串表示跟随全局默认；没给就保留现有的按产品配置。
// 只影响之后新发的码。被选用但还没就绪的服务商照样保存，在 not_ready 里列出来给界面警告（发码接口会拦）。
func AdminPutProviderConfig(c *gin.Context) {
	var req struct {
		Default   string             `json:"default"`
		ByProduct *map[string]string `json:"by_product"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式无效"})
		return
	}
	if strings.TrimSpace(req.Default) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择全局默认服务商"})
		return
	}
	prev, err := provider.LoadConfig()
	if err != nil {
		log.Printf("[provider] 读取服务商配置失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取服务商配置失败"})
		return
	}
	by := map[string]string{}
	if req.ByProduct != nil {
		by = *req.ByProduct
	} else {
		for k, v := range prev.ByProduct {
			by[k] = string(v)
		}
	}
	def, byNames, err := provider.ValidateConfig(req.Default, by)
	if err != nil {
		if errors.Is(err, provider.ErrInvalidConfig) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		log.Printf("[provider] 校验服务商配置失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}
	if err := provider.SaveConfig(def, byNames); err != nil {
		log.Printf("[provider] 保存服务商配置失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}
	auditAdmin(c, "provider_config_update", fmt.Sprintf("default %s→%s by_product %s→%s",
		prev.Default, def, byProductText(prev.ByProduct), byProductText(byNames)))

	cfg, err := provider.LoadConfig()
	if err != nil {
		log.Printf("[provider] 读取服务商配置失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "已保存，但读取配置失败，请刷新页面"})
		return
	}
	resp := providersOverview(cfg)
	resp["not_ready"] = notReadySelected(c, cfg)
	c.JSON(http.StatusOK, resp)
}

// byProductText 是审计里的按产品配置，按产品排序，如 gpt=marswallex,x=testup；空的写 -。
func byProductText(by map[string]provider.Name) string {
	if len(by) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+string(by[k]))
	}
	return strings.Join(parts, ",")
}

// notReadySelected 列出配置里被选用（全局默认或任一产品）但还没就绪的服务商，按注册顺序。
func notReadySelected(c *gin.Context, cfg provider.Config) []gin.H {
	selected := map[provider.Name]bool{cfg.Default: true}
	for _, n := range cfg.ByProduct {
		selected[n] = true
	}
	out := []gin.H{}
	for _, p := range provider.List() {
		if !selected[p.Name()] {
			continue
		}
		st := checkProvider(c.Request.Context(), p, "", "")
		if !st.Readiness.OK {
			out = append(out, gin.H{"provider": p.Name(), "problems": nonNilStrings(st.Readiness.Problems)})
		}
	}
	return out
}
