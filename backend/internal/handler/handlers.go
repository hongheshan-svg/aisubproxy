package handler

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/tuzi/cdk-recharge-system/internal/auth"
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

// ===== Recharge Portal APIs =====

// adminLoginRateKey 管理员登录失败限流的键：来源（IPv6 按 /64 归并）+ 小写去空白的用户名。
func adminLoginRateKey(ip, username string) string {
	return rateIPKey(ip) + "|" + strings.ToLower(strings.TrimSpace(username))
}

func AdminLogin(c *gin.Context) {
	var req AdminLoginRequest
	if !bindAuthJSON(c, &req, "Invalid request") {
		return
	}

	// 登录限流：按 客户端IP + 用户名 计数
	rateKey := adminLoginRateKey(c.ClientIP(), req.Username)
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
	// 密码两侧空白常因复制粘贴产生，校验时 Trim；安装（自设密码）和改密入库前也 Trim，口径一致
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
	// 和登录一样去掉首尾空白再校验、入库（审计 A-9）
	req.OldPassword = strings.TrimSpace(req.OldPassword)
	req.NewPassword = strings.TrimSpace(req.NewPassword)

	if utf8.RuneCountInString(req.NewPassword) < 12 || db.IsWeakPassword(req.NewPassword) {
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

	if ok, _ := verifyAdminPassword(storedHash, req.OldPassword); !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "旧密码不正确"})
		return
	}

	if err := db.SetAdminPassword(username, req.NewPassword); err != nil {
		log.Printf("change password error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "修改密码失败"})
		return
	}

	auditAdmin(c, "change_password", "")
	// 改密成功：随机初始密码文件（如有）已经没用，删掉
	db.RemoveInitialAdminPasswordFile()
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

// ListAuditLogs 返回管理员操作审计日志，按 id 倒序。
// before_id > 0 时只返回 id 小于它的记录（游标分页）；多取一条用来判断后面还有没有，has_more 为 true 时
// 用返回的 next_before_id 继续翻。
func ListAuditLogs(c *gin.Context) {
	limit := parseLimit(c.Query("limit"), 200)
	var beforeID int64
	if raw := strings.TrimSpace(c.Query("before_id")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "before_id 不合法"})
			return
		}
		beforeID = v
	}
	query := `
		SELECT id, COALESCE(username,''), COALESCE(action,''), COALESCE(detail,''), COALESCE(ip,''), COALESCE(created_at,'')
		FROM admin_audit_logs`
	args := []any{}
	if beforeID > 0 {
		query += ` WHERE id < ?`
		args = append(args, beforeID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := db.DB.Query(query, args...)
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
	hasMore := len(logs) > limit
	if hasMore {
		logs = logs[:limit]
	}
	var nextBeforeID int64
	if hasMore && len(logs) > 0 {
		nextBeforeID = logs[len(logs)-1].ID
	}
	c.JSON(http.StatusOK, gin.H{"logs": logs, "has_more": hasMore, "next_before_id": nextBeforeID})
}
