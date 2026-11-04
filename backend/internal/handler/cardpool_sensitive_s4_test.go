package handler

import (
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

// S4 / C-3：查看卡号只给前 6 后 4 的打码卡号，响应里没有完整卡号、CVV2、有效期。
func TestS4SensitiveEndpointReturnsMaskedOnly(t *testing.T) {
	c, mw, _ := cpEnv(t, false)
	cpInsertCard(t, c, "c1", cardpool.PoolAvailable, "")
	mw.On("GET", "/v1/card/sensitive/c1", mwtest.OK(`{"card_id":"c1","card_no":"`+cpFullPAN+`","cvv2":"987","expiry_date":"1228"}`))
	if w := cpDo(AdminCardPoolLeaseCard, "POST", "/c/:id", "/c/c1", ``); w.Code != 200 {
		t.Fatalf("lease %d %s", w.Code, w.Body.String())
	}
	w := cpDo(AdminCardPoolSensitive, "POST", "/c/:id", "/c/c1", ``)
	if w.Code != 200 {
		t.Fatalf("sensitive %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, bad := range []string{cpFullPAN, "987", "1228", "cvv2", "expiry"} {
		if strings.Contains(body, bad) {
			t.Errorf("响应不应含 %q: %s", bad, body)
		}
	}
	m := mwJSON(t, w)
	if len(m) != 1 || m["card_no_masked"] != "411111******1234" {
		t.Errorf("应只有打码卡号: %v", m)
	}
}

func TestS4MaskCardNumber(t *testing.T) {
	for in, want := range map[string]string{
		"4111111111111234":    "411111******1234",
		"4111 1111 1111 1234": "411111******1234",
		"341111111111234":     "341111*****1234",
		"1234":                "****",
		"":                    "",
	} {
		if got := s4MaskCardNumber(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
