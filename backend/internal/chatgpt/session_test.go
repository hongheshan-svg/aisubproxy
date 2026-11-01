package chatgpt

import (
	"errors"
	"strings"
	"testing"
)

func TestParseSession(t *testing.T) {
	full := `{"user":{"email":"  Foo@Example.COM "},"accessToken":"at-1","sessionToken":"st-1"}`
	alias := `{"user":{"email":"foo@example.com"},"access_token":"at-1","session_token":"st-1"}`
	want := Session{AccessToken: "at-1", SessionToken: "st-1", Email: "foo@example.com"}

	ok := []struct{ name, raw string }{
		{"原样 JSON", full},
		{"前后空白", "\n  " + full + " \n\t"},
		{"下划线别名", alias},
	}
	for _, c := range ok {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseSession(c.raw)
			if err != nil {
				t.Fatalf("意外错误: %v", err)
			}
			if got != want {
				t.Fatalf("got %+v want %+v", got, want)
			}
		})
	}

	bad := []struct{ name, raw, contains string }{
		{"缺 sessionToken", `{"user":{"email":"a@b.c"},"accessToken":"at"}`, "sessionToken"},
		{"缺邮箱", `{"accessToken":"at","sessionToken":"st"}`, "邮箱"},
		{"纯 accessToken", "eyJhbGciOi.abc.def", ""},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseSession(c.raw)
			if !errors.Is(err, ErrBadSession) {
				t.Fatalf("期望 ErrBadSession, got %v", err)
			}
			if c.contains != "" && !strings.Contains(err.Error(), c.contains) {
				t.Fatalf("错误消息 %q 应含 %q", err.Error(), c.contains)
			}
		})
	}
}

func TestSessionCookiesChunks(t *testing.T) {
	long := strings.Repeat("a", 3933) + strings.Repeat("b", 3933) + strings.Repeat("c", 134)
	cs := SessionCookies(long)
	if len(cs) != 3 {
		t.Fatalf("分片数 = %d, want 3", len(cs))
	}
	var sb strings.Builder
	for i, c := range cs {
		wantName := "__Secure-next-auth.session-token." + string(rune('0'+i))
		if c.Name != wantName {
			t.Fatalf("cookie[%d] 名 %q, want %q", i, c.Name, wantName)
		}
		sb.WriteString(c.Value)
	}
	if sb.String() != long {
		t.Fatal("拼接结果与原值不符")
	}

	short := SessionCookies("short")
	if len(short) != 1 || short[0].Name != "__Secure-next-auth.session-token" || short[0].Value != "short" {
		t.Fatalf("短 token 结果错误: %+v", short)
	}
}
