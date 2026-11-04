package handler

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/tuzi/cdk-recharge-system/internal/auth"
	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/notify"
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

// ConfirmTaskRequest request for confirming a task
type ConfirmTaskRequest struct {
	TaskID string `json:"task_id" binding:"required"`
}

// authBodyMax 是登录、改密、安装接口请求体的上限（审计 A-5）：这些接口只收用户名和密码，8 KB 足够。
// 它们不需要登录就能调（改密除外），不设上限的话一个几百 MB 的字符串字段会被整个读进内存。
const authBodyMax = 8 << 10

const authBodyTooLargeText = "请求太大"

// bindAuthJSON 限制请求体大小后再解析 JSON：超限 413，其余解析失败 400（文案 badRequest）。返回 false 时响应已写好。
func bindAuthJSON(c *gin.Context, obj any, badRequest string) bool {
	if c.Request.ContentLength > authBodyMax {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": authBodyTooLargeText})
		return false
	}
	if c.Request.Body != nil {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, authBodyMax)
	}
	if err := c.ShouldBindJSON(obj); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": authBodyTooLargeText})
			return false
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": badRequest})
		return false
	}
	return true
}

// verifyAdminPassword 是登录、安装自检用的密码校验，测试可替换。
var verifyAdminPassword = db.VerifyAdminPassword

var (
	dummyHashOnce sync.Once
	dummyHash     string
)

// dummyAdminHash 是用户名不存在时拿来比对的假 bcrypt 哈希（和真哈希同样的代价），只算一次。
func dummyAdminHash() string {
	dummyHashOnce.Do(func() {
		h, err := db.HashAdminPassword(newCSRFToken())
		if err != nil {
			log.Printf("生成登录用假哈希失败: %v", err)
			return
		}
		dummyHash = h
	})
	return dummyHash
}

type AdminLoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// ===== Recharge Portal APIs =====

func AdminLogin(c *gin.Context) {
	var req AdminLoginRequest
	if !bindAuthJSON(c, &req, "Invalid request") {
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
	var tokenVersion int64

	err := db.DB.QueryRow(`
		SELECT id, username, password_hash, display_name, is_active, token_version
		FROM admin_users
		WHERE username = ?
	`, strings.TrimSpace(req.Username)).Scan(&userID, &username, &passwordHash, &displayName, &isActive, &tokenVersion)
	if err != nil && err != sql.ErrNoRows {
		log.Printf("admin login lookup error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "系统错误"})
		return
	}
	// 用户名不存在、账号禁用、密码错误三种情况：都跑一次 bcrypt（不存在的用户比一个固定的假哈希），
	// 都计一次失败，都回同一个 401。响应内容和耗时都不暴露用户名是否存在（审计 A-8）。
	known := err == nil
	if !known {
		passwordHash = dummyAdminHash()
	}
	// 密码两侧空白常因复制粘贴产生，校验时 Trim
	plain := strings.TrimSpace(req.Password)
	ok, upgradedHash := verifyAdminPassword(passwordHash, plain)
	if !known || !ok || isActive != 1 {
		if known && ok {
			log.Printf("admin login: 账号 %s 已禁用，按登录失败处理", username)
		}
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
	sess, err := issueAdminSession(c, userID, username, name, tokenVersion)
	if err != nil {
		log.Printf("admin login session error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "系统错误"})
		return
	}
	c.JSON(http.StatusOK, sess)
}

// issueAdminSession 签发 JWT + HttpOnly cookie，供登录与安装向导共用。
// 令牌只放在 HttpOnly cookie 里，不进响应 JSON：前端 JS 拿不到，XSS 偷不走（审计 A-1）。
// 脚本和命令行仍可用 Authorization: Bearer，令牌另行签发，不经浏览器。
// tokenVersion 是校验密码时一并读出的令牌版本号，签进令牌，之后改密码就会让它作废（审计 A-2）。
func issueAdminSession(c *gin.Context, userID int64, username, displayName string, tokenVersion int64) (gin.H, error) {
	expiration := time.Now().Add(24 * time.Hour)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.CustomClaims{
		UserID:       userID,
		IsAdmin:      true,
		Username:     username,
		TokenVersion: tokenVersion,
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

// AdminMe 返回当前会话的管理员信息。前端刷新页面后靠它确认登录态、恢复用户名和到期时间。
func AdminMe(c *gin.Context) {
	userID, _ := c.Get("user_id")
	username, _ := c.Get("username")
	isAdmin, _ := c.Get("is_admin")
	expiresAt, _ := c.Get("token_expires_at")

	name, _ := username.(string)
	if id, ok := userID.(int64); ok {
		var display sql.NullString
		if err := db.DB.QueryRow(`SELECT display_name FROM admin_users WHERE id = ?`, id).Scan(&display); err == nil &&
			strings.TrimSpace(display.String) != "" {
			name = display.String
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"user_id":    userID,
		"username":   username,
		"name":       name,
		"is_admin":   isAdmin,
		"expires_at": expiresAt,
	})
}

// AdminChangePassword 让已登录管理员修改自己的密码。
func AdminChangePassword(c *gin.Context) {
	type ChangePasswordRequest struct {
		OldPassword string `json:"old_password" binding:"required"`
		NewPassword string `json:"new_password" binding:"required"`
	}

	var req ChangePasswordRequest
	if !bindAuthJSON(c, &req, "Invalid request") {
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
	// 版本号已加一，包括本机在内的所有旧会话都已作废；顺手清掉本机 cookie
	clearAuthCookies(c)
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

// ConfirmRechargeTask confirms and submits a recharge task
func ConfirmRechargeTask(c *gin.Context) {
	var req ConfirmTaskRequest
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	req.TaskID = strings.TrimSpace(req.TaskID)

	// Get task info
	var cdkCode string
	var sessionJSON sql.NullString
	err := db.DB.QueryRow(`
		SELECT cdk_code, session_json FROM recharge_tasks WHERE task_id = ? AND task_status = 'pending'
	`, req.TaskID).Scan(&cdkCode, &sessionJSON)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Task not found or already confirmed"})
		return
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "System error"})
		return
	}

	// Begin transaction
	tx, err := db.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to start transaction"})
		return
	}
	defer tx.Rollback()

	// Update task status to submitted
	_, err = tx.Exec(`
		UPDATE recharge_tasks
		SET task_status = 'submitted', updated_at = CURRENT_TIMESTAMP
		WHERE task_id = ?
	`, req.TaskID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update task"})
		return
	}

	// Mark CDK as used
	_, err = tx.Exec(`
		UPDATE cd_keys
		SET status = 'used', used_at = CURRENT_TIMESTAMP
		WHERE code = ?
	`, cdkCode)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to mark CDK"})
		return
	}

	if err = tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to commit"})
		return
	}

	// 订单成交：CDK 已使用 + session 已提交 → 推送 Telegram 通知（异步，不阻塞响应）
	notify.NotifyNewOrder(req.TaskID, cdkCode, sessionJSON.String)

	c.JSON(http.StatusOK, gin.H{
		"task_id": req.TaskID,
		"status":  "submitted",
		"message": "充值申请已提交，请等待处理",
	})
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
