package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

const (
	marsWallexProdURL = "https://api.feibeiglobal.com/portal/api"
	marsWallexTestURL = "https://api.feibeiglobal-test.com/portal/api"
)

type marsWallexPreset struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

func marsWallexPresets() []marsWallexPreset {
	return []marsWallexPreset{
		{ID: "prod", Label: "生产", URL: marsWallexProdURL},
		{ID: "test", Label: "测试（地址未确认）", URL: marsWallexTestURL},
	}
}

func marsWallexConfigErr(msg string) error {
	return &marswallex.Error{Outcome: marswallex.OutcomeNotSent, Reason: marswallex.ReasonConfig, Message: msg}
}

// marsWallexClientFromSettings 从设置构造客户端；任何失败返回 OutcomeNotSent/ReasonConfig 的 *marswallex.Error。
func marsWallexClientFromSettings() (*marswallex.Client, error) {
	baseURL, err := db.GetSetting(db.SettingMarsWallexBaseURL)
	if err != nil {
		return nil, marsWallexConfigErr("读取接口地址失败")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = marsWallexProdURL
	}
	devID, err := db.GetSetting(db.SettingMarsWallexDeveloperID)
	if err != nil {
		return nil, marsWallexConfigErr("读取 developerId 失败")
	}
	token, err := db.GetSecretSetting(db.SettingMarsWallexAPIToken)
	if err != nil {
		return nil, marsWallexConfigErr("读取 apiToken 失败（请检查 APP_MASTER_KEY）")
	}
	privPEM, err := db.GetSecretSetting(db.SettingMarsWallexPrivateKey)
	if err != nil {
		return nil, marsWallexConfigErr("读取私钥失败（请检查 APP_MASTER_KEY）")
	}
	pubPEM, err := db.GetSetting(db.SettingMarsWallexPlatformPublicKey)
	if err != nil {
		return nil, marsWallexConfigErr("读取平台公钥失败")
	}
	switch {
	case strings.TrimSpace(devID) == "":
		return nil, marsWallexConfigErr("未配置 developerId")
	case token == "":
		return nil, marsWallexConfigErr("未配置 apiToken")
	case privPEM == "":
		return nil, marsWallexConfigErr("未配置私钥")
	case strings.TrimSpace(pubPEM) == "":
		return nil, marsWallexConfigErr("未配置平台公钥")
	}
	priv, err := marswallex.ParsePrivateKey(privPEM)
	if err != nil {
		return nil, marsWallexConfigErr("私钥无法解析")
	}
	pub, err := marswallex.ParsePublicKey(pubPEM)
	if err != nil {
		return nil, marsWallexConfigErr("平台公钥无法解析")
	}
	cl, err := marswallex.NewClient(marswallex.Config{
		BaseURL: baseURL, DeveloperID: devID, APIToken: token, PrivateKey: priv, PlatformPublicKey: pub,
	})
	if err != nil {
		var me *marswallex.Error
		if errors.As(err, &me) {
			return nil, marsWallexConfigErr(me.Message)
		}
		return nil, marsWallexConfigErr("客户端配置无效")
	}
	return cl, nil
}

func marsWallexWebhookURL(c *gin.Context) string {
	scheme := "http"
	if p := strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Proto"), ",")[0]); p != "" {
		scheme = p
	} else if c.Request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host + "/api/v1/webhooks/marswallex"
}

// marsWallexSetting 读取设置；出错时记日志（不含值）并按未设置处理。
func marsWallexSetting(key string) string {
	v, err := db.GetSetting(key)
	if err != nil {
		log.Printf("marswallex config: read %s failed: %v", key, err)
		return ""
	}
	return v
}

func marsWallexConfigView(c *gin.Context) gin.H {
	baseURL := marsWallexSetting(db.SettingMarsWallexBaseURL)
	devID := marsWallexSetting(db.SettingMarsWallexDeveloperID)
	tokenRaw := marsWallexSetting(db.SettingMarsWallexAPIToken)
	privRaw := marsWallexSetting(db.SettingMarsWallexPrivateKey)
	platPEM := marsWallexSetting(db.SettingMarsWallexPlatformPublicKey)

	view := gin.H{
		"base_url":                        baseURL,
		"base_url_presets":                marsWallexPresets(),
		"developer_id":                    devID,
		"api_token_set":                   tokenRaw != "",
		"private_key_set":                 privRaw != "",
		"private_key_error":               "",
		"public_key_pem":                  "",
		"public_key_fingerprint":          "",
		"platform_public_key_set":         strings.TrimSpace(platPEM) != "",
		"platform_public_key_fingerprint": "",
		"master_key_ready":                true,
		"master_key_error":                "",
		"webhook_url":                     marsWallexWebhookURL(c),
	}
	if _, err := secretbox.LoadKey(); err != nil {
		view["master_key_ready"] = false
		view["master_key_error"] = err.Error()
		if privRaw != "" {
			view["private_key_error"] = "服务器的 APP_MASTER_KEY 不可用，已保存的私钥无法解密"
		}
	} else if privRaw != "" {
		pemText, err := db.GetSecretSetting(db.SettingMarsWallexPrivateKey)
		if err != nil {
			// 只记错误，不记密文或明文。
			log.Printf("marswallex config: decrypt private key failed: %v", err)
			view["private_key_error"] = "已保存的私钥无法解密，请检查 APP_MASTER_KEY 或重新录入私钥"
		} else if priv, err := marswallex.ParsePrivateKey(pemText); err != nil {
			view["private_key_error"] = "已保存的私钥无法解析，请重新录入私钥"
		} else {
			view["public_key_pem"] = marswallex.PublicKeyPEM(&priv.PublicKey)
			view["public_key_fingerprint"] = marswallex.Fingerprint(&priv.PublicKey)
		}
	}
	if strings.TrimSpace(platPEM) != "" {
		if pub, err := marswallex.ParsePublicKey(platPEM); err == nil {
			view["platform_public_key_fingerprint"] = marswallex.Fingerprint(pub)
		}
	}
	return view
}

// AdminGetMarsWallexConfig 返回配置（不含任何密钥内容）。
func AdminGetMarsWallexConfig(c *gin.Context) {
	c.JSON(http.StatusOK, marsWallexConfigView(c))
}

type marsWallexConfigReq struct {
	BaseURL           *string `json:"base_url"`
	DeveloperID       *string `json:"developer_id"`
	APIToken          *string `json:"api_token"`
	PrivateKey        *string `json:"private_key"`
	PlatformPublicKey *string `json:"platform_public_key"`
}

// AdminPutMarsWallexConfig 保存配置：先全部校验，通过后才写库；api_token/private_key 空串表示不修改。
func AdminPutMarsWallexConfig(c *gin.Context) {
	var req marsWallexConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	bad := func(msg string) { c.JSON(http.StatusBadRequest, gin.H{"error": msg}) }

	var baseURL string
	if req.BaseURL != nil {
		baseURL = strings.TrimSpace(*req.BaseURL)
		if baseURL != "" {
			if err := marswallex.ValidateBaseURL(baseURL); err != nil {
				bad("接口地址无效：" + err.Error())
				return
			}
		}
	}
	setToken := req.APIToken != nil && *req.APIToken != ""
	setPriv := req.PrivateKey != nil && strings.TrimSpace(*req.PrivateKey) != ""
	if setPriv {
		if _, err := marswallex.ParsePrivateKey(*req.PrivateKey); err != nil {
			bad("私钥无法解析：" + err.Error())
			return
		}
	}
	setPlat := req.PlatformPublicKey != nil && strings.TrimSpace(*req.PlatformPublicKey) != ""
	if setPlat {
		if _, err := marswallex.ParsePublicKey(*req.PlatformPublicKey); err != nil {
			bad("平台公钥无法解析：" + err.Error())
			return
		}
	}
	if setToken || setPriv {
		if _, err := secretbox.LoadKey(); err != nil {
			if errors.Is(err, secretbox.ErrNoMasterKey) {
				bad("服务器未配置 APP_MASTER_KEY，无法保存密钥")
			} else {
				bad("服务器的 APP_MASTER_KEY 格式不正确，无法保存密钥")
			}
			return
		}
	}

	var changed []string
	fail := func(field string, err error) {
		log.Printf("marswallex config: save %s failed: %v", field, err)
		// 前面已写入的字段同样要审计（只记字段名）。
		if len(changed) > 0 {
			auditAdmin(c, "update_marswallex_config", "partial (failed at "+field+"); changed: "+strings.Join(changed, ","))
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
	}
	if req.BaseURL != nil {
		if err := db.SetSetting(db.SettingMarsWallexBaseURL, baseURL); err != nil {
			fail("base_url", err)
			return
		}
		changed = append(changed, "base_url")
	}
	if req.DeveloperID != nil {
		if err := db.SetSetting(db.SettingMarsWallexDeveloperID, strings.TrimSpace(*req.DeveloperID)); err != nil {
			fail("developer_id", err)
			return
		}
		changed = append(changed, "developer_id")
	}
	if setToken {
		if err := db.SetSecretSetting(db.SettingMarsWallexAPIToken, *req.APIToken); err != nil {
			fail("api_token", err)
			return
		}
		changed = append(changed, "api_token")
	}
	if setPriv {
		if err := db.SetSecretSetting(db.SettingMarsWallexPrivateKey, *req.PrivateKey); err != nil {
			fail("private_key", err)
			return
		}
		changed = append(changed, "private_key")
	}
	if setPlat {
		if err := db.SetSetting(db.SettingMarsWallexPlatformPublicKey, strings.TrimSpace(*req.PlatformPublicKey)); err != nil {
			fail("platform_public_key", err)
			return
		}
		changed = append(changed, "platform_public_key")
	}
	if len(changed) > 0 {
		auditAdmin(c, "update_marswallex_config", "changed: "+strings.Join(changed, ","))
	}
	c.JSON(http.StatusOK, marsWallexConfigView(c))
}

func marsWallexHint(me *marswallex.Error) string {
	switch {
	case me.Outcome == marswallex.OutcomeNotSent && me.Reason == marswallex.ReasonConfig:
		return "配置不完整：" + me.Message
	case me.Outcome == marswallex.OutcomeNotSent && (me.Reason == marswallex.ReasonDNS || me.Reason == marswallex.ReasonDial):
		return "无法连接 MarsWallex，请检查服务器网络和接口地址"
	case me.HTTPStatus == http.StatusUnauthorized || me.HTTPStatus == http.StatusForbidden:
		return "认证或签名被拒：请检查 developerId、apiToken，并确认交给 MarsWallex 的公钥与此处私钥是同一对"
	case me.HTTPStatus == http.StatusNotFound:
		return "接口地址不对：请检查地址是否包含 /portal/api 前缀"
	case me.Reason == marswallex.ReasonSignatureMissing || me.Reason == marswallex.ReasonSignatureInvalid:
		return "响应签名验证失败：请检查平台公钥是否正确"
	case me.Reason == marswallex.ReasonTimeout || me.Reason == marswallex.ReasonConnection:
		return "请求超时或连接中断，请稍后重试"
	}
	return fmt.Sprintf("MarsWallex 返回 HTTP %d：%s", me.HTTPStatus, me.Message)
}

// AdminTestMarsWallex 用 ListAccounts 测试连接；HTTP 恒 200，结果在 body 中。
func AdminTestMarsWallex(c *gin.Context) {
	resp := gin.H{"outcome": "", "reason": "", "http_status": 0, "code": "", "message": "", "hint": "", "accounts": []gin.H{}}
	fillErr := func(err error) {
		var me *marswallex.Error
		if !errors.As(err, &me) {
			me = &marswallex.Error{Outcome: marswallex.OutcomeUnknown, Reason: marswallex.ReasonConnection, Message: "请求失败"}
		}
		resp["outcome"] = me.Outcome.String()
		resp["reason"] = string(me.Reason)
		resp["http_status"] = me.HTTPStatus
		resp["code"] = me.Code
		resp["message"] = me.Message
		resp["hint"] = marsWallexHint(me)
	}
	cl, err := marsWallexClientFromSettings()
	if err != nil {
		fillErr(err)
		c.JSON(http.StatusOK, resp)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	page, err := cl.ListAccounts(ctx, marswallex.ListAccountsQuery{PageNo: 1, PageSize: 10})
	if err != nil {
		fillErr(err)
		c.JSON(http.StatusOK, resp)
		return
	}
	accounts := make([]gin.H, 0, len(page.Data))
	for _, a := range page.Data {
		bals := make([]gin.H, 0, len(a.BalanceList))
		for _, b := range a.BalanceList {
			bals = append(bals, gin.H{"currency": b.Currency, "available_balance": b.AvailableBalance, "freeze_balance": b.FreezeBalance})
		}
		accounts = append(accounts, gin.H{
			"account_no": a.AccountNo, "account_name": a.AccountName, "account_type": a.AccountType,
			"account_status": a.AccountStatus, "balances": bals,
		})
	}
	resp["outcome"] = marswallex.OutcomeSuccess.String()
	resp["hint"] = "连接成功"
	resp["accounts"] = accounts
	c.JSON(http.StatusOK, resp)
}
