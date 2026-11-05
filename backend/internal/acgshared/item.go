package acgshared

import (
	"net/url"
	"strings"
)

// ItemDescription 是商品对象里固定的描述。
const ItemDescription = "<p>购买后获得兑换码，卡密里附有兑换链接，打开即可兑换。</p>"

const maxNameRunes = 255

// ItemInput 是构造商品对象所需的数据。
type ItemInput struct {
	ID         int64
	Code, Name string
	Sort       int
	PriceCents int64
	Stock      int
}

// Item 是共享协议里的商品对象。
type Item struct {
	ID              int64  `json:"id"`
	Code            string `json:"code"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Cover           string `json:"cover"`
	Price           Money  `json:"price"`
	UserPrice       int    `json:"user_price"`
	FactoryPrice    Money  `json:"factory_price"`
	Stock           int    `json:"stock"`
	Config          string `json:"config"`
	Widget          string `json:"widget"`
	DeliveryWay     int    `json:"delivery_way"`
	ContactType     int    `json:"contact_type"`
	PasswordStatus  int    `json:"password_status"`
	Minimum         int    `json:"minimum"`
	Maximum         int    `json:"maximum"`
	OnlyUser        int    `json:"only_user"`
	PurchaseCount   int    `json:"purchase_count"`
	DraftStatus     int    `json:"draft_status"`
	DraftPremium    int    `json:"draft_premium"`
	InventoryHidden int    `json:"inventory_hidden"`
	SeckillStatus   int    `json:"seckill_status"`
	Sort            int    `json:"sort"`
	Status          int    `json:"status"`
	CategoryID      int    `json:"category_id"`
}

// NewItem 构造商品对象；名字为空用 code，超过 255 个字按字截断。
func NewItem(in ItemInput) Item {
	name := in.Name
	if name == "" {
		name = in.Code
	}
	if r := []rune(name); len(r) > maxNameRunes {
		name = string(r[:maxNameRunes])
	}
	return Item{
		ID: in.ID, Code: in.Code, Name: name, Description: ItemDescription,
		Price: Money(in.PriceCents), FactoryPrice: Money(in.PriceCents),
		Stock: in.Stock, Widget: "[]", Sort: in.Sort, Status: 1, CategoryID: 1,
	}
}

// Category 是共享协议里的商品分类。
type Category struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Sort     int    `json:"sort"`
	Icon     string `json:"icon"`
	Status   int    `json:"status"`
	PID      int    `json:"pid"`
	Children []Item `json:"children"`
}

// AllCategory 返回恰好一个「全部商品」分类；items 为 nil 时 children 输出 []。
func AllCategory(items []Item) []Category {
	if items == nil {
		items = []Item{}
	}
	return []Category{{ID: 1, Name: "全部商品", Status: 1, Children: items}}
}

// SecretText 生成发货文本：每行「码  兑换：https://host/recharge?code=码」。
func SecretText(host string, codes []string) string {
	lines := make([]string, len(codes))
	for i, c := range codes {
		lines[i] = c + "  兑换：https://" + host + "/recharge?code=" + url.QueryEscape(c)
	}
	return strings.Join(lines, "\n")
}
