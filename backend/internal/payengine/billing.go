package payengine

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// taxFreeState 是一个美国无销售税州：城市与对应的邮编前三位。
type taxFreeState struct {
	state  string
	cities [][2]string // {城市, 邮编前缀}
}

// taxFreeStates 取自原型 automation/lib/address.js。
var taxFreeStates = []taxFreeState{
	{"DE", [][2]string{{"Wilmington", "198"}, {"Newark", "197"}, {"Dover", "199"}}},
	{"OR", [][2]string{{"Portland", "972"}, {"Salem", "973"}, {"Eugene", "974"}}},
	{"NH", [][2]string{{"Manchester", "031"}, {"Nashua", "030"}, {"Concord", "033"}}},
	{"MT", [][2]string{{"Billings", "591"}, {"Missoula", "598"}, {"Bozeman", "597"}}},
	{"AK", [][2]string{{"Anchorage", "995"}, {"Fairbanks", "997"}, {"Juneau", "998"}}},
}

var streets = []string{"Main St", "Oak Ave", "Maple Dr", "Pine St", "Cedar Ln", "Elm St", "Washington Ave", "Park Rd", "Lake Dr", "Hill St"}

// TaxFreeAddress 随机生成一个美国免税州（DE / OR / NH / MT / AK）的账单地址。Name 留空由调用方填。
func TaxFreeAddress(r *rand.Rand) paybrowser.Billing {
	st := taxFreeStates[r.IntN(len(taxFreeStates))]
	city := st.cities[r.IntN(len(st.cities))]
	return paybrowser.Billing{
		Line1:   fmt.Sprintf("%d %s", 10+r.IntN(9990), streets[r.IntN(len(streets))]),
		City:    city[0],
		State:   st.state,
		Zip:     fmt.Sprintf("%s%02d", city[1], r.IntN(100)),
		Country: "US",
	}
}

var (
	expiryMMYY    = regexp.MustCompile(`^(\d{2})(\d{2})$`)
	expirySlash   = regexp.MustCompile(`^(\d{1,2})\s*/\s*(\d{2}|\d{4})$`)
	expiryISOLike = regexp.MustCompile(`^(\d{4})-(\d{1,2})$`)
)

// parseExpiry 把卡有效期（"0828"、"08/28"、"08/2028"、"2028-08"）解析为两位月份与两位年份。
func parseExpiry(s string) (mm, yy string, err error) {
	s = strings.TrimSpace(s)
	var month, year string
	if m := expiryMMYY.FindStringSubmatch(s); m != nil {
		month, year = m[1], m[2]
	} else if m := expirySlash.FindStringSubmatch(s); m != nil {
		month, year = m[1], m[2]
	} else if m := expiryISOLike.FindStringSubmatch(s); m != nil {
		month, year = m[2], m[1]
	} else {
		return "", "", fmt.Errorf("卡有效期格式无法识别")
	}
	n, _ := strconv.Atoi(month)
	if n < 1 || n > 12 {
		return "", "", fmt.Errorf("卡有效期月份无效")
	}
	return fmt.Sprintf("%02d", n), year[len(year)-2:], nil
}
