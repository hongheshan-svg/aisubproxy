package db

import "testing"

func TestIsWeakPassword_Weak(t *testing.T) {
	for _, p := range []string{
		// 原有规则：太短、名单原文
		"", "short", "admin123456", "password123", "123456789012", "qwerty123456",
		// 常见词加首尾数字或符号
		"password1234", "Password1234!", "admin1234567", "Admin@2026!!!", "!!welcome2026",
		"adminadmin2026", "passw0rd2026", "qwerty123456789", "letmein123456", "changeme2026!",
		"  password1234  ",
		// 同一字符重复
		"aaaaaaaaaaaa", "111111111111", "............", "éééééééééééé",
		// 连续数字（升序、降序，可跨 9→0）
		"1234567890123", "987654321098", "098765432109",
	} {
		if !IsWeakPassword(p) {
			t.Errorf("%q 应判为弱口令", p)
		}
	}
}

func TestIsWeakPassword_Strong(t *testing.T) {
	for _, p := range []string{
		"Correct-Horse-Battery-Staple",
		"InitPass-12345",
		"correct horse battery",
		"welcome-to-the-jungle",
		"Adm1n-Rotat3-Key",
		"829104736251",
		"1234567890ab",
	} {
		if IsWeakPassword(p) {
			t.Errorf("%q 不应判为弱口令", p)
		}
	}
}
