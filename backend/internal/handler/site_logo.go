package handler

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

const (
	siteLogoKey      = "site_logo"
	siteLogoTypeKey  = "site_logo_type"
	siteLogoVerKey   = "site_logo_version"
	siteLogoMaxBytes = 256 << 10
)

const siteLogoTooBig = "Logo 不能超过 256 KB，请换一张小一点的图片"

var siteLogoTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true}

// siteLogoVersion 取内容 SHA-256 的前 12 位十六进制；没有 Logo 时为空串。
func siteLogoVersion(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// loadSiteLogo 读出库里的 Logo、类型和版本号；三者缺一或内容不合法都当作没有 Logo。
func loadSiteLogo() (b []byte, typ, version string) {
	raw, _ := db.GetSetting(siteLogoKey)
	typ, _ = db.GetSetting(siteLogoTypeKey)
	version, _ = db.GetSetting(siteLogoVerKey)
	if raw == "" || version == "" || !siteLogoTypes[typ] {
		return nil, "", ""
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(b) == 0 {
		return nil, "", ""
	}
	return b, typ, version
}

// currentLogoVersion 只读版本号，不解码图片；缺类型或内容时同样当作没有 Logo。
func currentLogoVersion() string {
	raw, _ := db.GetSetting(siteLogoKey)
	typ, _ := db.GetSetting(siteLogoTypeKey)
	version, _ := db.GetSetting(siteLogoVerKey)
	if raw == "" || version == "" || !siteLogoTypes[typ] {
		return ""
	}
	return version
}

// AdminUploadSiteLogo POST /api/v1/admin/settings/logo — multipart 字段 file，只收 PNG、JPEG、WebP。
func AdminUploadSiteLogo(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, siteLogoMaxBytes+64<<10)
	fh, err := c.FormFile("file")
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			c.JSON(http.StatusBadRequest, gin.H{"error": siteLogoTooBig})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有收到图片，请选择一张 PNG、JPG 或 WebP 图片再上传"})
		return
	}
	f, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读不了上传的文件，请重新选择图片再试"})
		return
	}
	defer f.Close()
	content, err := io.ReadAll(io.LimitReader(f, siteLogoMaxBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读不了上传的文件，请重新选择图片再试"})
		return
	}
	if len(content) > siteLogoMaxBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": siteLogoTooBig})
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
		siteLogoVerKey:  siteLogoVersion(content),
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}
	auditAdmin(c, "update_site_logo", typ+" "+strconv.Itoa(len(content)))
	c.JSON(http.StatusOK, gin.H{"logo_version": siteLogoVersion(content)})
}

// AdminDeleteSiteLogo DELETE /api/v1/admin/settings/logo
func AdminDeleteSiteLogo(c *gin.Context) {
	for _, k := range []string{siteLogoKey, siteLogoTypeKey, siteLogoVerKey} {
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
	b, typ, version := loadSiteLogo()
	if b == nil {
		c.Status(http.StatusNotFound)
		return
	}
	etag := `"` + version + `"`
	c.Header("ETag", etag)
	c.Header("Cache-Control", "public, max-age=300")
	c.Header("X-Content-Type-Options", "nosniff")
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, typ, b)
}
