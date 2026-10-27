package cardpool

import (
	"context"
	"database/sql"
	"errors"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// Cardholder 对应 mw_cardholders。
type Cardholder struct {
	CardHolderID string
	FirstName    string
	LastName     string
	CountryCode  string
	Status       string
	Remark       string
	Note         string
	SyncedAt     *int64
	CreatedAt    int64
}

// Card 对应 mw_cards。
type Card struct {
	CardID          string
	CardHolderID    string
	ProductID       string
	CardLabel       string
	CardCountry     string
	Currency        string
	Last4           string
	Expiry          string
	PlatformStatus  string
	AvailableCents  int64
	FrozenCents     int64
	BalanceSyncedAt *int64
	Source          string
	PoolState       string
	LeaseToken      string
	LeaseOwner      string
	LeaseExpiresAt  *int64
	LeaseSensitive  bool
	UseCount        int
	LastSensitiveAt *int64
	RetireReason    string
	DisabledReason  string
	FrozenAt        *int64
	CancelAfter     *int64
	NextActionAt    *int64
	CreatedAt       int64
	UpdatedAt       int64
}

// Order 对应 mw_card_orders。
type Order struct {
	ID            int64
	OutOrderNo    string
	Kind          string
	CardID        string
	CardHolderID  string
	ProductID     string
	AmountCents   int64
	Currency      string
	TargetStatus  string
	State         string
	PlatformTxnID string
	EverUnknown   bool
	Attempts      int
	NextCheckAt   *int64
	FirstSentAt   *int64
	LastError     string
	RequestedBy   string
	Note          string
	AlertedStale  bool
	CreatedAt     int64
	UpdatedAt     int64
	FinishedAt    *int64
}

// Transaction 对应 mw_card_transactions；金额按平台原字符串保存，仅用于展示。
type Transaction struct {
	TransactionID       string
	Kind                string
	CardID              string
	Status              string
	DeclineReason       string
	TxnAmount           string
	TxnCurrency         string
	BillingAmount       string
	BillingCurrency     string
	MerchantName        string
	OriginTransactionID string
	AuthTransactionID   string
	OccurredAt          *int64
	UpdatedAt           int64
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
