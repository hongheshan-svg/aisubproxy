package marswallex

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
)

// maxUploadSize 为上传文件大小上限（7MB）。
const maxUploadSize = 7 << 20

// UploadResult 为上传文件响应。
type UploadResult struct {
	FileID string `json:"file_id"`
}

// UploadFile 上传文件（multipart，仅含 file 字段）。biz_type 在查询串中并参与签名，
// 请求体不参与签名。超过 7MB 直接返回 NotSent 错误，不发请求。
func (c *Client) UploadFile(ctx context.Context, bizType, filename string, content []byte) (*UploadResult, error) {
	if len(content) > maxUploadSize {
		return nil, &Error{Outcome: OutcomeNotSent, Reason: ReasonConfig, Message: "文件超过 7MB"}
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return nil, configError("构造上传请求失败", err)
	}
	if _, err := fw.Write(content); err != nil {
		return nil, configError("构造上传请求失败", err)
	}
	if err := mw.Close(); err != nil {
		return nil, configError("构造上传请求失败", err)
	}
	out := &UploadResult{}
	if err := c.send(ctx, http.MethodPost, "/v1/file/upload", buildQuery("biz_type", bizType), mw.FormDataContentType(), buf.Bytes(), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}
