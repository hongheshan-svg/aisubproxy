package marswallex

import (
	"context"
	"net/http"
	"net/url"
)

// Address 为地址（国家为 ISO 3166-1 alpha-2 两位码）。
type Address struct {
	Country  string `json:"country"`
	State    string `json:"state"`
	City     string `json:"city"`
	Address  string `json:"address"`
	PostCode string `json:"post_code"`
}

// CardholderProfile 为持卡人资料（添加持卡人请求体去掉 out_card_holder_id）。
// 日期为 YYYY-MM-DD；IDType: PASSPORT / IDCARD / OTHER_CERTIFICATE；SSN 仅国家为 US 时必填。
type CardholderProfile struct {
	FirstName          string   `json:"first_name"`
	LastName           string   `json:"last_name"`
	ChineseFirstName   string   `json:"chinese_first_name,omitempty"`
	ChineseLastName    string   `json:"chinese_last_name,omitempty"`
	DateOfBirth        string   `json:"date_of_birth"`
	ZoneNumber         string   `json:"zone_number"`
	Mobile             string   `json:"mobile"`
	Email              string   `json:"email"`
	CountryCode        string   `json:"country_code"`
	IDType             string   `json:"id_type"`
	IDNo               string   `json:"id_no"`
	IDPeriodBegin      string   `json:"id_period_begin"`
	IDPeriodEnd        string   `json:"id_period_end"`
	IDPhotoFileIDs     []string `json:"id_photo_file_ids"`
	ResidentialAddress Address  `json:"residential_address"`
	BillingAddress     Address  `json:"billing_address"`
	SSN                string   `json:"ssn,omitempty"`
}

// CreateCardholderRequest 为添加持卡人参数，Profile 字段内嵌展开。
type CreateCardholderRequest struct {
	OutCardHolderID string `json:"out_card_holder_id"`
	CardholderProfile
}

// CardholderRef 为添加 / 修改持卡人响应。
type CardholderRef struct {
	CardHolderID string `json:"card_holder_id"`
}

// CardholderDetail 为持卡人详情。Status: PENDING_REVIEW / APPROVED / REJECTED / SUSPENDED / DISABLED。
type CardholderDetail struct {
	CardHolderID    string `json:"card_holder_id"`
	OutCardHolderID string `json:"out_card_holder_id"`
	CardholderProfile
	Status string `json:"status"`
	Remark string `json:"remark,omitempty"`
}

// CardholderStatusResult 为修改持卡人状态响应。
type CardholderStatusResult struct {
	CardHolderID    string `json:"card_holder_id"`
	OutCardHolderID string `json:"out_card_holder_id"`
	Status          string `json:"status"`
}

// CreateCardholder 添加持卡人。
func (c *Client) CreateCardholder(ctx context.Context, req CreateCardholderRequest) (*CardholderRef, error) {
	out := &CardholderRef{}
	if err := c.call(ctx, http.MethodPost, "/v1/cardholder", "", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetCardholder 按平台持卡人 ID 查询。
func (c *Client) GetCardholder(ctx context.Context, cardHolderID string) (*CardholderDetail, error) {
	out := &CardholderDetail{}
	if err := c.call(ctx, http.MethodGet, "/v1/cardholder/"+url.PathEscape(cardHolderID), "", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateCardholder 修改持卡人资料（请求体不含 out_card_holder_id）。
func (c *Client) UpdateCardholder(ctx context.Context, cardHolderID string, p CardholderProfile) (*CardholderRef, error) {
	out := &CardholderRef{}
	if err := c.call(ctx, http.MethodPut, "/v1/cardholder/"+url.PathEscape(cardHolderID), "", p, out); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateCardholderStatus 启用（APPROVED）或禁用（DISABLED）持卡人。
func (c *Client) UpdateCardholderStatus(ctx context.Context, cardHolderID, status string) (*CardholderStatusResult, error) {
	body := struct {
		Status string `json:"status"`
	}{status}
	out := &CardholderStatusResult{}
	if err := c.call(ctx, http.MethodPut, "/v1/cardholder/status/"+url.PathEscape(cardHolderID), "", body, out); err != nil {
		return nil, err
	}
	return out, nil
}
