package payengine

import (
	"math/rand/v2"
	"regexp"
	"testing"
)

func TestParseExpiry(t *testing.T) {
	for _, in := range []string{"0828", "08/28", "08/2028", "2028-08", " 08 / 28 "} {
		mm, yy, err := parseExpiry(in)
		if err != nil || mm != "08" || yy != "28" {
			t.Fatalf("%q 应解析为 08/28，得到 %q %q %v", in, mm, yy, err)
		}
	}
	if mm, yy, err := parseExpiry("8/31"); err != nil || mm != "08" || yy != "31" {
		t.Fatalf("单位数月份应补零: %q %q %v", mm, yy, err)
	}
	for _, in := range []string{"13/28", "abc", "", "00/28", "1328", "08-28", "2028/08", "08/202"} {
		if _, _, err := parseExpiry(in); err == nil {
			t.Fatalf("%q 应报错", in)
		}
	}
}

func TestTaxFreeAddress(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	states := map[string]bool{"DE": true, "OR": true, "NH": true, "MT": true, "AK": true}
	zipRe := regexp.MustCompile(`^\d{5}$`)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		a := TaxFreeAddress(r)
		if !states[a.State] || a.Country != "US" || !zipRe.MatchString(a.Zip) || a.Line1 == "" || a.City == "" || a.Name != "" {
			t.Fatalf("地址不符: %+v", a)
		}
		seen[a.State] = true
	}
	if len(seen) != len(states) {
		t.Fatalf("100 次应覆盖全部免税州: %v", seen)
	}
}
