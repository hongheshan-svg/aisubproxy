package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func s7Ctx() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)
	return c, w
}

// N-1：嵌套的 data.order 和 events[*] 里的卡号、CVV、有效期都要剔除。
func TestS7StripOrderCardNumberNested(t *testing.T) {
	c, _ := s7Ctx()
	var m map[string]any
	body := `{"order":{"id":1,"card_number":"5378727109708264","cvv":"123","expiry":"12/30"},
	  "events":[{"type":"x","pan":"5378727109708264","cvv2":"999"},{"deep":[{"card_number":"4111111111111111"}]}]}`
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	enrichOneCDKOrder(c, nil, m)
	b, _ := json.Marshal(m)
	s := string(b)
	for _, bad := range []string{"5378727109708264", "4111111111111111", "card_number", `"pan"`, "cvv", "expiry", "123", "12/30"} {
		if strings.Contains(s, bad) {
			t.Fatalf("嵌套结构仍含 %q: %s", bad, s)
		}
	}
	if !strings.Contains(s, `"card_last_four":"8264"`) || !strings.Contains(s, `"card_last_four":"1111"`) {
		t.Fatalf("应补 card_last_four: %s", s)
	}
}

// N-1：上游返回无法解析的 body 时不透传，返回 502。
func TestS7WriteCDKOrderUnparsableIs502(t *testing.T) {
	c, w := s7Ctx()
	writeCDKOrderRaw(c, nil, []byte(`not json card_number 5378727109708264`))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("应 502，实际 %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "5378727109708264") {
		t.Fatalf("响应透传了原文: %s", w.Body.String())
	}
}
