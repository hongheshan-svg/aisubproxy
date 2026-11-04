package server

import "testing"

// A-3：app.env.example 里的占位值和带 change-me/example 字样的密钥，release 模式必须拒绝启动。
func TestEnforceJWTSecretRejectsExamplePlaceholders(t *testing.T) {
	cases := []struct {
		name   string
		secret string
	}{
		{"示例占位值原样", "change-me-to-random-32chars-min"},
		{"示例占位值大写", "CHANGE-ME-TO-RANDOM-32CHARS-MIN"},
		{"含 change-me 子串", "prefix-change-me-suffix-0123456789"},
		{"含 changeme 子串", "xxchangemexx0123456789abcdef"},
		{"含 example 子串", "my-example-secret-0123456789"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("JWT_SECRET", tc.secret)
			if err := enforceJWTSecret("release", false); err == nil {
				t.Fatalf("release 模式应拒绝示例密钥 %q", tc.secret)
			}
			if err := enforceJWTSecret("debug", true); err == nil {
				t.Fatalf("已配置上游时应拒绝示例密钥 %q", tc.secret)
			}
		})
	}
}
