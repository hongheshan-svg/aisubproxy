package cardpool

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

// CardPreview 是导入卡之前的只读预览。平台的卡详情会返回完整卡号，这里只留前 6 位与后 4 位，
// 其余卡号不返回、不落库、不写日志与审计。
type CardPreview struct {
	CardID             string   `json:"card_id"`
	CardLabel          string   `json:"card_label"`
	CardCountry        string   `json:"card_country"`
	CardCurrency       string   `json:"card_currency"`
	CardMode           string   `json:"card_mode"`
	Status             string   `json:"status"`
	AvailableCents     int64    `json:"available_cents"`
	FrozenCents        int64    `json:"frozen_cents"`
	Expiry             string   `json:"expiry"`
	First6             string   `json:"first6"`
	Last4              string   `json:"last4"`
	AlreadyInPool      bool     `json:"already_in_pool"`
	SuggestedProductID string   `json:"suggested_product_id"`
	Candidates         []string `json:"candidates"`
}

// PreviewCard 调 GetCard 与 ListCardProducts 给出卡信息和可选产品；不写库。
func (s *Service) PreviewCard(ctx context.Context, cardID string) (*CardPreview, error) {
	cardID = strings.TrimSpace(cardID)
	if cardID == "" {
		return nil, badRequest("卡 ID 不能为空")
	}
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	d, err := c.GetCard(ctx, cardID)
	if err != nil {
		return nil, redactPAN(err)
	}
	products, err := c.ListCardProducts(ctx)
	if err != nil {
		return nil, redactPAN(err)
	}
	avail, err := parseBalance(d.AvailableBalance)
	if err != nil {
		return nil, errors.New("解析可用余额失败")
	}
	frozen, err := parseBalance(d.FreezeBalance)
	if err != nil {
		return nil, errors.New("解析冻结余额失败")
	}
	inPool := true
	if _, err := getCard(ctx, cardID); errors.Is(err, ErrNotFound) {
		inPool = false
	} else if err != nil {
		return nil, err
	}
	first6, last4 := panParts(d.CardNo)
	suggested, cands := suggestProduct(*d, products)
	return &CardPreview{
		CardID: cardID, CardLabel: d.CardLabel, CardCountry: d.CardCountry, CardCurrency: d.CardCurrency, CardMode: d.CardMode,
		Status: normStatus(d.Status), AvailableCents: avail, FrozenCents: frozen, Expiry: d.ExpiryDate,
		First6: first6, Last4: last4, AlreadyInPool: inPool, SuggestedProductID: suggested, Candidates: cands,
	}, nil
}

var longDigits = regexp.MustCompile(`\d(?:[ -]?\d){11,}`)

// redactPAN 去掉平台错误说明里夹带的长数字串（可能是完整卡号），其余字段原样保留，错误类型不变。
func redactPAN(err error) error {
	var me *marswallex.Error
	if errors.As(err, &me) {
		cp := *me
		cp.Message = longDigits.ReplaceAllString(me.Message, "****")
		return &cp
	}
	return err
}

// panParts 取卡号开头连续 6 位数字作前 6 位（忽略空格与连字符）；后 4 位直接用导入时存库的 lastDigits4，保证预览与导入后一致。
func panParts(cardNo string) (first6, last4 string) {
	clean := strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(cardNo))
	i := 0
	for i < len(clean) && isDigit(clean[i]) {
		i++
	}
	if i >= 6 {
		first6 = clean[:6]
	}
	return first6, lastDigits4(cardNo)
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// suggestProduct 先在卡组、国家、币种、模式都一致的产品里找候选；只有一个就选它。
// 有多个时，选产品名里有一段至少 6 位的纯数字、且它的前 6 位与卡号前 6 位相同的那个；选不出唯一一个就返回空。
func suggestProduct(d marswallex.CardDetail, products []marswallex.CardProduct) (string, []string) {
	eq := strings.EqualFold
	t := strings.TrimSpace
	cands := []string{}
	var matched []string
	first6, _ := panParts(d.CardNo)
	for _, p := range products {
		if !eq(t(d.CardLabel), t(p.CardLabel)) || !eq(t(d.CardCountry), t(p.CardCountry)) ||
			!eq(t(d.CardCurrency), t(p.CardCurrency)) || !eq(t(d.CardMode), t(p.CardMode)) {
			continue
		}
		cands = append(cands, p.ProductID)
		if first6 != "" && nameHasPrefixRun(p.ProductName, first6) {
			matched = append(matched, p.ProductID)
		}
	}
	if len(cands) == 1 {
		return cands[0], cands
	}
	if len(matched) == 1 {
		return matched[0], cands
	}
	return "", cands
}

// nameHasPrefixRun 产品名里是否有一段至少 6 位的数字，其前 6 位等于 first6。
func nameHasPrefixRun(name, first6 string) bool {
	for i := 0; i < len(name); {
		if !isDigit(name[i]) {
			i++
			continue
		}
		j := i
		for j < len(name) && isDigit(name[j]) {
			j++
		}
		if j-i >= 6 && name[i:i+6] == first6 {
			return true
		}
		i = j
	}
	return false
}
