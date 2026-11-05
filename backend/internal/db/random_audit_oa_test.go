package db

import (
	"bytes"
	"strings"
	"testing"
)

// A-17：随机口令用拒绝采样，每个字符等概率。字节 >= 224（56 的最大整数倍）直接丢掉，不再取模。
func TestOARandomPasswordRejectionSampling(t *testing.T) {
	// 250、224 被丢掉；5 → 'F'，223 → 223%56=55 → '9'，0 → 'A'
	src := bytes.NewReader([]byte{250, 5, 223, 224, 0, 1, 2, 3})
	got, err := randomStringFrom(src, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got != "F9A" {
		t.Fatalf("got %q want %q", got, "F9A")
	}
	if _, err := randomStringFrom(bytes.NewReader([]byte{255, 255}), 3); err == nil {
		t.Fatal("随机源不够时应返回错误，不能退回可预测的口令")
	}
}

func TestOARandomPasswordAlphabet(t *testing.T) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"
	for i := 0; i < 50; i++ {
		p := randomPassword(24)
		if len(p) != 24 {
			t.Fatalf("长度 %d", len(p))
		}
		for _, r := range p {
			if !strings.ContainsRune(alphabet, r) {
				t.Fatalf("口令含字母表外字符 %q", r)
			}
		}
	}
}
