package chatgpt

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// HTTPError 是 ChatGPT 返回的非 2xx 响应（401、403 和 Cloudflare 拦截除外，它们有各自的错误）。
// Detail 是响应里能说明原因的那部分，已打码、截断；没有时为空。
type HTTPError struct {
	Status     int
	RetryAfter int // 秒；响应没给或给的不是秒数时为 0
	Detail     string
}

func (e *HTTPError) Error() string {
	msg := fmt.Sprintf("ChatGPT 返回 HTTP %d", e.Status)
	if e.RetryAfter > 0 {
		msg += fmt.Sprintf("（建议 %d 秒后再试）", e.RetryAfter)
	}
	if e.Detail != "" {
		msg += "：" + e.Detail
	}
	return msg
}

const maxDetailRunes = 200

func newHTTPError(status int, contentType, retryAfter string, body []byte) *HTTPError {
	e := &HTTPError{Status: status, Detail: errorDetail(contentType, body)}
	if n, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && n > 0 && n <= 86400 {
		e.RetryAfter = n
	}
	return e
}

var (
	htmlTitleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	// 令牌和长串不透出：JWT（访问令牌）、sess-/sk-/rt- 开头的令牌、40 位以上的不透明串
	tokenRes = []*regexp.Regexp{
		regexp.MustCompile(`eyJ[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*`),
		regexp.MustCompile(`\b(?:sess|sk|rt)-[A-Za-z0-9_-]{8,}`),
		regexp.MustCompile(`[A-Za-z0-9_\-+/=]{40,}`),
	}
)

// errorDetail 从错误响应里取说明：JSON 取 detail、error、message、code；纯文本取短正文；HTML 只取标题。
func errorDetail(contentType string, body []byte) string {
	ct := strings.ToLower(contentType)
	var d string
	switch {
	case strings.Contains(ct, "json"):
		var v map[string]any
		if json.Unmarshal(body, &v) == nil {
			d = jsonDetail(v)
		}
	case strings.Contains(ct, "text/html"):
		head := body
		if len(head) > 8192 {
			head = head[:8192]
		}
		if m := htmlTitleRe.FindSubmatch(head); m != nil {
			d = string(m[1])
		}
	case strings.Contains(ct, "text/plain"):
		if len(body) <= 500 {
			d = string(body)
		}
	}
	d = strings.Join(strings.Fields(d), " ")
	for _, re := range tokenRes {
		d = re.ReplaceAllString(d, "***")
	}
	if utf8.RuneCountInString(d) > maxDetailRunes {
		d = string([]rune(d)[:maxDetailRunes]) + "…"
	}
	return d
}

func jsonDetail(v map[string]any) string {
	for _, k := range []string{"detail", "error"} {
		if s := describe(v[k]); s != "" {
			return s
		}
	}
	return withCode(str(v["message"]), str(v["code"]))
}

// describe 把 detail / error 的值转成一句话：字符串原样；对象取 message（或 msg）和 code；
// 数组是 FastAPI 的校验错误，取前 3 条的「字段位置: 说明」。
func describe(x any) string {
	switch t := x.(type) {
	case string:
		return t
	case map[string]any:
		msg := str(t["message"])
		if msg == "" {
			msg = str(t["msg"])
		}
		return withCode(msg, str(t["code"]))
	case []any:
		var parts []string
		for _, it := range t {
			if len(parts) == 3 {
				break
			}
			switch e := it.(type) {
			case string:
				parts = append(parts, e)
			case map[string]any:
				msg := str(e["msg"])
				if msg == "" {
					msg = str(e["message"])
				}
				if loc, ok := e["loc"].([]any); ok && len(loc) > 0 {
					var segs []string
					for _, l := range loc {
						segs = append(segs, fmt.Sprint(l))
					}
					msg = strings.Join(segs, ".") + ": " + msg
				}
				if msg != "" {
					parts = append(parts, msg)
				}
			}
		}
		return strings.Join(parts, "; ")
	}
	return ""
}

func withCode(msg, code string) string {
	switch {
	case msg != "" && code != "":
		return msg + "（" + code + "）"
	case msg != "":
		return msg
	}
	return code
}

func str(x any) string {
	s, _ := x.(string)
	return strings.TrimSpace(s)
}
