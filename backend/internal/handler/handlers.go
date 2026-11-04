package handler

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/tuzi/cdk-recharge-system/internal/auth"
	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

func parseLimit(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}

	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}

	if value > 500 {
		return 500
	}

	return value
}

type AdminLoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type AdminLoginResponse struct {
	Token     string `json:"token"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	ExpiresAt string `json:"expires_at"`
}

// ===== Recharge Portal APIs =====

func AdminLogin(c *gin.Context) {
	var req AdminLoginRequest
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	// 登录限流：按 客户端IP + 用户名 计数
	rateKey := c.ClientIP() + "|" + strings.ToLower(strings.TrimSpace(req.Username))
	if ok, wait := loginAllowed(rateKey); !ok {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": fmt.Sprintf("尝试次数过多，请 %d 分钟后再试", int(wait.Minutes())+1),
		})
		return
	}

	var userID int64
	var username string
	var passwordHash string
	var displayName sql.NullString
	var isActive int

	err := db.DB.QueryRow(`
		SELECT id, username, password_hash, display_name, is_active
		FROM admin_users
		WHERE username = ?
	`, strings.TrimSpace(req.Username)).Scan(&userID, &username, &passwordHash, &displayName, &isActive)
	if err == sql.ErrNoRows {
		loginFailed(rateKey)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}
	if err != nil {
		log.Printf("admin login lookup error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "系统错误"})
		return
	}
	if isActive != 1 {
		c.JSON(http.StatusForbidden, gin.H{"error": "账号已被禁用"})
		return
	}
	// 密码两侧空白常因复制粘贴产生，校验时 Trim；入库仍按原样（生成口令无空白）
	plain := strings.TrimSpace(req.Password)
	ok, upgradedHash := db.VerifyAdminPassword(passwordHash, plain)
	if !ok {
		loginFailed(rateKey)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}
	loginSucceeded(rateKey)
	// 旧 SHA-256 哈希登录成功后，无感升级为 bcrypt
	db.UpgradeAdminHash(username, upgradedHash)
	db.WriteAudit(username, "login", "", c.ClientIP())

	name := username
	if displayName.Valid && strings.TrimSpace(displayName.String) != "" {
		name = displayName.String
	}
	sess, err := issueAdminSession(c, userID, username, name)
	if err != nil {
		log.Printf("admin login session error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "系统错误"})
		return
	}
	c.JSON(http.StatusOK, sess)
}

// issueAdminSession 签发 JWT + HttpOnly cookie，供登录与安装向导共用。
func issueAdminSession(c *gin.Context, userID int64, username, displayName string) (gin.H, error) {
	expiration := time.Now().Add(24 * time.Hour)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.CustomClaims{
		UserID:   userID,
		IsAdmin:  true,
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiration),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Subject:   username,
		},
	})
	signedToken, err := token.SignedString([]byte(auth.JWTSecret()))
	if err != nil {
		return nil, err
	}
	if displayName == "" {
		displayName = username
	}
	csrf := newCSRFToken()
	setAuthCookies(c, signedToken, csrf, int(time.Until(expiration).Seconds()))
	return gin.H{
		"token":      signedToken,
		"username":   username,
		"name":       displayName,
		"expires_at": expiration.Format(time.RFC3339),
		"csrf_token": csrf,
	}, nil
}

// AdminLogout 清除认证 cookie。
func AdminLogout(c *gin.Context) {
	clearAuthCookies(c)
	c.JSON(http.StatusOK, gin.H{"message": "已退出登录"})
}

func AdminMe(c *gin.Context) {
	userID, _ := c.Get("user_id")
	username, _ := c.Get("username")
	isAdmin, _ := c.Get("is_admin")

	c.JSON(http.StatusOK, gin.H{
		"user_id":  userID,
		"username": username,
		"is_admin": isAdmin,
	})
}

// AdminChangePassword 让已登录管理员修改自己的密码。
func AdminChangePassword(c *gin.Context) {
	type ChangePasswordRequest struct {
		OldPassword string `json:"old_password" binding:"required"`
		NewPassword string `json:"new_password" binding:"required"`
	}

	var req ChangePasswordRequest
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	if len(req.NewPassword) < 12 || db.IsWeakPassword(req.NewPassword) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "新密码至少 12 位，且不能为常见弱口令"})
		return
	}
	if req.NewPassword == req.OldPassword {
		c.JSON(http.StatusBadRequest, gin.H{"error": "新密码不能与旧密码相同"})
		return
	}

	usernameVal, _ := c.Get("username")
	username, _ := usernameVal.(string)
	if username == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未识别的登录身份"})
		return
	}

	var storedHash string
	if err := db.DB.QueryRow(`SELECT password_hash FROM admin_users WHERE username = ?`, username).Scan(&storedHash); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "系统错误"})
		return
	}

	if ok, _ := db.VerifyAdminPassword(storedHash, req.OldPassword); !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "旧密码不正确"})
		return
	}

	if err := db.SetAdminPassword(username, req.NewPassword); err != nil {
		log.Printf("change password error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "修改密码失败"})
		return
	}

	auditAdmin(c, "change_password", "")
	c.JSON(http.StatusOK, gin.H{"message": "密码已修改，请用新密码重新登录"})
}

// auditAdmin 记录当前登录管理员的一次操作。
func auditAdmin(c *gin.Context, action, detail string) {
	u, _ := c.Get("username")
	username, _ := u.(string)
	db.WriteAudit(username, action, detail, c.ClientIP())
}

// ListAuditLogs 返回最近的管理员操作审计日志。
func ListAuditLogs(c *gin.Context) {
	limit := parseLimit(c.Query("limit"), 200)
	rows, err := db.DB.Query(`
		SELECT id, COALESCE(username,''), COALESCE(action,''), COALESCE(detail,''), COALESCE(ip,''), COALESCE(created_at,'')
		FROM admin_audit_logs
		ORDER BY id DESC LIMIT ?
	`, limit)
	if err != nil {
		log.Printf("list audit error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list audit logs"})
		return
	}
	defer rows.Close()

	type AuditRecord struct {
		ID        int64  `json:"id"`
		Username  string `json:"username"`
		Action    string `json:"action"`
		Detail    string `json:"detail"`
		IP        string `json:"ip"`
		CreatedAt string `json:"created_at"`
	}
	logs := make([]AuditRecord, 0)
	for rows.Next() {
		var item AuditRecord
		if err := rows.Scan(&item.ID, &item.Username, &item.Action, &item.Detail, &item.IP, &item.CreatedAt); err != nil {
			continue
		}
		logs = append(logs, item)
	}
	c.JSON(http.StatusOK, gin.H{"logs": logs})
}

// ===== Billing Tool APIs =====

// ===== System Stats =====

// GetSystemStats 管理后台总览：CDK / 兑换订单以卡台 Open API 为准（不再读本地过渡表）。
func GetSystemStats(c *gin.Context) {
	out := gin.H{
		"source":           "cardplatform",
		"configured":       false,
		"total_cdks":       0,
		"unused_cdks":      0,
		"active_cdks":      0, // 兼容旧字段 = unused
		"active_cdkeys":    0, // 兼容前端曾用的字段名
		"total_cdkeys":     0, // 兼容
		"reserved_cdks":    0,
		"consumed_cdks":    0,
		"frozen_cdks":      0,
		"disabled_cdks":    0,
		"total_cdk_orders": 0,
		"webhook_events":   0,
		"counts_partial":   false,
		"error":            "",
	}

	var webhookCount int
	if db.DB != nil {
		_ = db.DB.QueryRow(`SELECT COUNT(*) FROM webhook_events`).Scan(&webhookCount)
	}
	out["webhook_events"] = webhookCount

	cfg := cardplatform.LoadConfig()
	if cfg.APIKey == "" {
		out["error"] = "未配置卡台 API Key"
		// 仍返回 200，前端展示提示
		c.JSON(http.StatusOK, out)
		return
	}
	out["configured"] = true

	cli := cardplatform.NewFromSettings()
	ctx := c.Request.Context()

	sum, err := cli.SummarizeCDKs(ctx, 50)
	if err != nil {
		out["error"] = err.Error()
		c.JSON(http.StatusOK, out)
		return
	}
	by := sum.ByStatus
	unused := by["unused"]
	out["total_cdks"] = sum.Total
	out["total_cdkeys"] = sum.Total
	out["unused_cdks"] = unused
	out["active_cdks"] = unused
	out["active_cdkeys"] = unused
	out["reserved_cdks"] = by["reserved"]
	out["consumed_cdks"] = by["consumed"]
	out["frozen_cdks"] = by["frozen"]
	out["disabled_cdks"] = by["disabled"]
	out["by_status"] = by
	out["counts_partial"] = sum.Partial
	out["scanned"] = sum.Scanned

	if ordersTotal, err := cli.ListCDKOrdersTotal(ctx); err == nil {
		out["total_cdk_orders"] = ordersTotal
	}

	// 兼容旧前端「任务数」字段：用卡台兑换订单数
	out["total_tasks"] = out["total_cdk_orders"]

	c.JSON(http.StatusOK, out)
}
