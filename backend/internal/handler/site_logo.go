package handler

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

const (
	siteLogoKey      = "site_logo"
	siteLogoTypeKey  = "site_logo_type"
	siteLogoMaxBytes = 256 << 10
)

var siteLogoTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true}

// siteLogoVersion 取内容 SHA-256 的前 12 位十六进制；没有 Logo 时为空串。
func siteLogoVersion(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// loadSiteLogo 读出库里的 Logo 和类型；没有或损坏都当作没有。
func loadSiteLogo() ([]byte, string) {
	raw, _ := db.GetSetting(siteLogoKey)
	typ, _ := db.GetSetting(siteLogoTypeKey)
	if raw == "" || !siteLogoTypes[typ] {
		return nil, ""
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(b) == 0 {
		return nil, ""
	}
	return b, typ
}

func currentLogoVersion() string {
	b, _ := loadSiteLogo()
	return siteLogoVersion(b)
}

// AdminUploadSiteLogo POST /api/v1/admin/settings/logo — multipart 字段 file，只收 PNG、JPEG、WebP。
func AdminUploadSiteLogo(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, siteLogoMaxBytes+64<<10)
	fh, err := c.FormFile("file")
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请以 multipart 字段 file 上传 Logo，且不能超过 256 KB"})
		return
	}
	f, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读不了上传的文件"})
		return
	}
	defer f.Close()
	content, err := io.ReadAll(io.LimitReader(f, siteLogoMaxBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读不了上传的文件"})
		return
	}
	if len(content) > siteLogoMaxBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Logo 不能超过 256 KB"})
		return
	}
	typ := http.DetectContentType(content)
	if !siteLogoTypes[typ] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Logo 只能是 PNG、JPG 或 WebP 图片"})
		return
	}
	if err := db.SetSettings(map[string]string{
		siteLogoKey:     base64.StdEncoding.EncodeToString(content),
		siteLogoTypeKey: typ,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}
	auditAdmin(c, "update_site_logo", typ+" "+strconv.Itoa(len(content)))
	c.JSON(http.StatusOK, gin.H{"logo_version": siteLogoVersion(content)})
}

// AdminDeleteSiteLogo DELETE /api/v1/admin/settings/logo
func AdminDeleteSiteLogo(c *gin.Context) {
	for _, k := range []string{siteLogoKey, siteLogoTypeKey} {
		if err := db.DeleteSetting(k); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
			return
		}
	}
	auditAdmin(c, "delete_site_logo", "")
	c.JSON(http.StatusOK, gin.H{"logo_version": ""})
}

// PublicSiteLogo GET /api/v1/public/site/logo
func PublicSiteLogo(c *gin.Context) {
	b, typ := loadSiteLogo()
	if b == nil {
		c.Status(http.StatusNotFound)
		return
	}
	etag := `"` + siteLogoVersion(b) + `"`
	c.Header("ETag", etag)
	c.Header("Cache-Control", "public, max-age=300")
	c.Header("X-Content-Type-Options", "nosniff")
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, typ, b)
}
