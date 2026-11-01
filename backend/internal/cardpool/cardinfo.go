package cardpool

import (
	"context"
	"database/sql"
	"errors"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// CardInfo 是付款填表要用的卡片非敏感信息：卡号后四位与持卡人英文名。
type CardInfo struct {
	Last4, HolderFirstName, HolderLastName string
}

// CardInfo 查卡号后四位与持卡人英文名。卡不存在返回 ErrNotFound；持卡人不存在时姓名为空，不报错。
func (s *Service) CardInfo(ctx context.Context, cardID string) (CardInfo, error) {
	var ci CardInfo
	err := db.DB.QueryRowContext(ctx, `SELECT c.last4, COALESCE(h.first_name, ''), COALESCE(h.last_name, '')
		FROM mw_cards c LEFT JOIN mw_cardholders h ON h.card_holder_id = c.card_holder_id
		WHERE c.card_id = ?`, cardID).Scan(&ci.Last4, &ci.HolderFirstName, &ci.HolderLastName)
	if errors.Is(err, sql.ErrNoRows) {
		return CardInfo{}, ErrNotFound
	}
	if err != nil {
		return CardInfo{}, err
	}
	return ci, nil
}
