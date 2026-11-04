package cardplatform

import "testing"

// C-1：card_api_base 只接受 https，本机回环地址除外。
func TestS1ValidateBase(t *testing.T) {
	ok := []string{"https://zovocard.com", "https://card.example.com/", "https://zovocard.com/openapi/v1", "http://127.0.0.1:9000", "http://localhost:8080", "https://127.0.0.1", "http://[::1]:80"}
	bad := []string{"http://zovocard.com", "http://169.254.169.254", "https://u:p@zovocard.com", "https://zovocard.com?x=1", "https://zovocard.com#f", "ftp://zovocard.com", "zovocard.com", "https://", "http://localhost.evil.com", "https://zovocard.com/a b"}
	for _, s := range ok {
		if err := ValidateBase(s); err != nil {
			t.Errorf("%q 应通过: %v", s, err)
		}
	}
	for _, s := range bad {
		if err := ValidateBase(s); err == nil {
			t.Errorf("%q 应拒绝", s)
		}
	}
}
