package handler

import (
	"errors"
	"log"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 登录、改密、安装向导共用的小工具（审计 OA 组）。

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
