package handler

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

// 持卡人资料（证件号、证件照、出生日期、SSN、手机、邮箱、地址）只经内存转发：
// 本文件的接口不记录请求体与响应体，出错时日志不带错误内容（平台错误信息可能回显资料），
// 审计只记持卡人 ID、商户侧 ID 与状态。

const (
	maxIDPhotoBytes  = 7 << 20        // 证件照单张上限
	maxIDPhotoUpload = 7<<20 + 64<<10 // 上传请求体上限（含表单开销）
	idPhotoTooLarge  = "文件超过 7MB"
)

// holderFail 与 poolFail 的映射相同，但内部错误只记方法与路由，不记错误内容。
func holderFail(c *gin.Context, err error) {
	status, body := poolErrorStatus(err)
	if status == http.StatusInternalServerError {
		log.Printf("cardpool api %s %s: 内部错误（持卡人资料接口不记录错误内容）", c.Request.Method, c.FullPath())
	}
	c.JSON(status, body)
}

// AdminCardPoolUploadPhoto 把一张证件照（multipart 字段 file）转发到平台，返回 file_id。文件只在内存中经过。
func AdminCardPoolUploadPhoto(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxIDPhotoUpload)
		fh, err := c.FormFile("file")
		if c.Request.MultipartForm != nil {
			defer c.Request.MultipartForm.RemoveAll()
		}
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": idPhotoTooLarge})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "请以 multipart 字段 file 上传证件照"})
			return
		}
		if fh.Size > maxIDPhotoBytes {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": idPhotoTooLarge})
			return
		}
		f, err := fh.Open()
		if err != nil {
			holderFail(c, err)
			return
		}
		defer f.Close()
		content, err := io.ReadAll(io.LimitReader(f, maxIDPhotoBytes+1))
		if err != nil {
			holderFail(c, err)
			return
		}
		if len(content) > maxIDPhotoBytes {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": idPhotoTooLarge})
			return
		}
		id, err := s.UploadIDPhoto(c.Request.Context(), fh.Filename, content)
		if err != nil {
			holderFail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"file_id": id})
	})(c)
}

// AdminCardPoolCreateCardholder 新建持卡人：成功 200 {submission}；平台结果未知 202 {submission, unknown: true}。
func AdminCardPoolCreateCardholder(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		var p marswallex.CardholderProfile
		if !bindPoolJSON(c, &p) {
			return
		}
		sub, err := s.CreateCardholder(c.Request.Context(), p, adminName(c))
		if sub != nil {
			poolAudit(c, "holder_create", fmt.Sprintf("out_card_holder_id=%s card_holder_id=%s", sub.OutCardHolderID, sub.CardHolderID))
		}
		if err != nil {
			if sub == nil {
				// 只记结果摘要，不带资料和平台错误原文
				poolAuditFailed(c, "holder_create", "country="+strings.TrimSpace(p.CountryCode), err)
			}
			holderFail(c, err)
			return
		}
		if sub.State == cardpool.SubmissionUnknown {
			c.JSON(http.StatusAccepted, gin.H{"submission": sub, "unknown": true})
			return
		}
		c.JSON(http.StatusOK, gin.H{"submission": sub})
	})(c)
}

// AdminCardPoolCardholderProfile 实时返回平台上的完整持卡人资料（修改时预填），不缓存、不落地。
// 成功返回时写审计，只记持卡人 ID，不记证件、电话等原文。
func AdminCardPoolCardholderProfile(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		c.Header("Cache-Control", "no-store")
		id := strings.TrimSpace(c.Param("id"))
		d, err := s.CardholderProfile(c.Request.Context(), id)
		if err != nil {
			holderFail(c, err)
			return
		}
		poolAudit(c, "holder_profile", "card_holder_id="+id)
		c.JSON(http.StatusOK, d)
	})(c)
}

// AdminCardPoolUpdateCardholder 全量修改持卡人资料，返回重新导入后的本地记录（不含资料）。
func AdminCardPoolUpdateCardholder(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		var p marswallex.CardholderProfile
		if !bindPoolJSON(c, &p) {
			return
		}
		h, err := s.UpdateCardholder(c.Request.Context(), c.Param("id"), p, adminName(c))
		if err != nil {
			poolAuditFailed(c, "holder_update", "card_holder_id="+strings.TrimSpace(c.Param("id")), err)
			holderFail(c, err)
			return
		}
		poolAudit(c, "holder_update", "card_holder_id="+h.CardHolderID)
		c.JSON(http.StatusOK, h)
	})(c)
}

// AdminCardPoolCardholderStatus 启用（APPROVED）或禁用（DISABLED）持卡人。
func AdminCardPoolCardholderStatus(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		var req struct {
			Status string `json:"status"`
		}
		if !bindPoolJSON(c, &req) {
			return
		}
		h, err := s.SetCardholderStatus(c.Request.Context(), c.Param("id"), req.Status, adminName(c))
		if err != nil {
			poolAuditFailed(c, "holder_status", fmt.Sprintf("card_holder_id=%s status=%s",
				strings.TrimSpace(c.Param("id")), strings.ToUpper(strings.TrimSpace(req.Status))), err)
			poolFail(c, err)
			return
		}
		poolAudit(c, "holder_status", fmt.Sprintf("card_holder_id=%s status=%s",
			h.CardHolderID, strings.ToUpper(strings.TrimSpace(req.Status))))
		c.JSON(http.StatusOK, h)
	})(c)
}

// AdminCardPoolSubmissions 列出未关联的持卡人提交记录。
func AdminCardPoolSubmissions(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		items, err := s.ListSubmissions(c.Request.Context())
		poolList(c, items, len(items), err)
	})(c)
}

// AdminCardPoolAbandonSubmission 放弃一条 submitted / unknown 提交记录。
func AdminCardPoolAbandonSubmission(c *gin.Context) {
	poolRoute(func(c *gin.Context, s *cardpool.Service) {
		outID := strings.TrimSpace(c.Param("out_id"))
		if err := s.AbandonSubmission(c.Request.Context(), outID, adminName(c)); err != nil {
			poolFail(c, err)
			return
		}
		poolAudit(c, "holder_abandon", "out_card_holder_id="+outID)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})(c)
}
