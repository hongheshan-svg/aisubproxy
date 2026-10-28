package cardpool

import (
	"context"
	"database/sql"
	"errors"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// Cardholder 对应 mw_cardholders。
type Cardholder struct {
	CardHolderID string `json:"card_holder_id"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	CountryCode  string `json:"country_code"`
	Status       string `json:"status"`
	Remark       string `json:"remark"`
	Note         string `json:"note"`
	SyncedAt     *int64 `json:"synced_at"`
	CreatedAt    int64  `json:"created_at"`
}

// Card 对应 mw_cards。
type Card struct {
	CardID          string `json:"card_id"`
	CardHolderID    string `json:"card_holder_id"`
	ProductID       string `json:"product_id"`
	CardLabel       string `json:"card_label"`
	CardCountry     string `json:"card_country"`
	Currency        string `json:"currency"`
	Last4           string `json:"last4"`
	Expiry          string `json:"expiry"`
	PlatformStatus  string `json:"platform_status"`
	AvailableCents  int64  `json:"available_cents"`
	FrozenCents     int64  `json:"frozen_cents"`
	BalanceSyncedAt *int64 `json:"balance_synced_at"`
	Source          string `json:"source"`
	PoolState       string `json:"pool_state"`
	LeaseToken      string `json:"-"`
	LeaseOwner      string `json:"lease_owner"`
	LeaseExpiresAt  *int64 `json:"lease_expires_at"`
	LeaseSensitive  bool   `json:"lease_sensitive"`
	UseCount        int    `json:"use_count"`
	LastSensitiveAt *int64 `json:"last_sensitive_at"`
	RetireReason    string `json:"retire_reason"`
	DisabledReason  string `json:"disabled_reason"`
	FrozenAt        *int64 `json:"frozen_at"`
	CancelAfter     *int64 `json:"cancel_after"`
	NextActionAt    *int64 `json:"next_action_at"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

// Order 对应 mw_card_orders。
type Order struct {
	ID            int64  `json:"id"`
	OutOrderNo    string `json:"out_order_no"`
	Kind          string `json:"kind"`
	CardID        string `json:"card_id"`
	CardHolderID  string `json:"card_holder_id"`
	ProductID     string `json:"product_id"`
	AmountCents   int64  `json:"amount_cents"`
	Currency      string `json:"currency"`
	TargetStatus  string `json:"target_status"`
	State         string `json:"state"`
	PlatformTxnID string `json:"platform_txn_id"`
	EverUnknown   bool   `json:"ever_unknown"`
	Attempts      int    `json:"attempts"`
	NextCheckAt   *int64 `json:"next_check_at"`
	FirstSentAt   *int64 `json:"first_sent_at"`
	LastError     string `json:"last_error"`
	RequestedBy   string `json:"requested_by"`
	Note          string `json:"note"`
	AlertedStale  bool   `json:"alerted_stale"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
	FinishedAt    *int64 `json:"finished_at"`
}

// Transaction 对应 mw_card_transactions；金额按平台原字符串保存，仅用于展示。
type Transaction struct {
	TransactionID       string `json:"transaction_id"`
	Kind                string `json:"kind"`
	CardID              string `json:"card_id"`
	Status              string `json:"status"`
	DeclineReason       string `json:"decline_reason"`
	TxnAmount           string `json:"txn_amount"`
	TxnCurrency         string `json:"txn_currency"`
	BillingAmount       string `json:"billing_amount"`
	BillingCurrency     string `json:"billing_currency"`
	MerchantName        string `json:"merchant_name"`
	OriginTransactionID string `json:"origin_transaction_id"`
	AuthTransactionID   string `json:"auth_transaction_id"`
	OccurredAt          *int64 `json:"occurred_at"`
	UpdatedAt           int64  `json:"updated_at"`
}

const cardholderCols = `card_holder_id, first_name, last_name, country_code, status, remark, note, synced_at, created_at`

const cardCols = `card_id, card_holder_id, product_id, card_label, card_country, currency, last4, expiry, platform_status,
	available_cents, frozen_cents, balance_synced_at, source, pool_state, lease_token, lease_owner, lease_expires_at,
	lease_sensitive, use_count, last_sensitive_at, retire_reason, disabled_reason, frozen_at, cancel_after, next_action_at,
	created_at, updated_at`

const orderCols = `id, out_order_no, kind, card_id, card_holder_id, product_id, amount_cents, currency, target_status,
	state, platform_txn_id, ever_unknown, attempts, next_check_at, first_sent_at, last_error, requested_by, note,
	alerted_stale, created_at, updated_at, finished_at`

type rowScanner interface{ Scan(dest ...any) error }

func (h *Cardholder) scan(r rowScanner) error {
	return r.Scan(&h.CardHolderID, &h.FirstName, &h.LastName, &h.CountryCode, &h.Status, &h.Remark, &h.Note, &h.SyncedAt, &h.CreatedAt)
}

func (c *Card) scan(r rowScanner) error {
	return r.Scan(&c.CardID, &c.CardHolderID, &c.ProductID, &c.CardLabel, &c.CardCountry, &c.Currency, &c.Last4, &c.Expiry,
		&c.PlatformStatus, &c.AvailableCents, &c.FrozenCents, &c.BalanceSyncedAt, &c.Source, &c.PoolState, &c.LeaseToken,
		&c.LeaseOwner, &c.LeaseExpiresAt, &c.LeaseSensitive, &c.UseCount, &c.LastSensitiveAt, &c.RetireReason,
		&c.DisabledReason, &c.FrozenAt, &c.CancelAfter, &c.NextActionAt, &c.CreatedAt, &c.UpdatedAt)
}

func (o *Order) scan(r rowScanner) error {
	return r.Scan(&o.ID, &o.OutOrderNo, &o.Kind, &o.CardID, &o.CardHolderID, &o.ProductID, &o.AmountCents, &o.Currency,
		&o.TargetStatus, &o.State, &o.PlatformTxnID, &o.EverUnknown, &o.Attempts, &o.NextCheckAt, &o.FirstSentAt,
		&o.LastError, &o.RequestedBy, &o.Note, &o.AlertedStale, &o.CreatedAt, &o.UpdatedAt, &o.FinishedAt)
}

func getCard(ctx context.Context, cardID string) (*Card, error) {
	var c Card
	err := c.scan(db.DB.QueryRowContext(ctx, `SELECT `+cardCols+` FROM mw_cards WHERE card_id = ?`, cardID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func getOrder(ctx context.Context, outOrderNo string) (*Order, error) {
	var o Order
	err := o.scan(db.DB.QueryRowContext(ctx, `SELECT `+orderCols+` FROM mw_card_orders WHERE out_order_no = ?`, outOrderNo))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func getCardholder(ctx context.Context, id string) (*Cardholder, error) {
	var h Cardholder
	err := h.scan(db.DB.QueryRowContext(ctx, `SELECT `+cardholderCols+` FROM mw_cardholders WHERE card_holder_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}
