package server

import "testing"

// 非 release 模式下，已配置动钱上游密钥时弱 JWT_SECRET 必须拒绝启动；未配置时只告警，仍可启动。
func TestEnforceJWTSecret(t *testing.T) {
	cases := []struct {
		name     string
		secret   string
		mode     string
		upstream bool
		wantErr  bool
	}{
		{"debug 无上游密钥、默认密钥仍可启动", "", "debug", false, false},
		{"debug 已配置上游、默认密钥拒绝", "", "debug", true, true},
		{"debug 已配置上游、过短密钥拒绝", "short", "debug", true, true},
		{"debug 已配置上游、强密钥通过", "0123456789abcdef0123", "debug", true, false},
		{"release 无上游、缺失密钥拒绝", "", "release", false, true},
		{"release 强密钥通过", "0123456789abcdef0123", "release", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("JWT_SECRET", tc.secret)
			err := enforceJWTSecret(tc.mode, tc.upstream)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
