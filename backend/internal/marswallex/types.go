package marswallex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var amountRe = regexp.MustCompile(`^\d{1,12}(\.\d{1,2})?$`)

// FormatAmount 将分换算为两位小数的金额字符串，负数报错。
func FormatAmount(minor int64) (string, error) {
	if minor < 0 {
		return "", errors.New("marswallex: 金额不能为负数")
	}
	return fmt.Sprintf("%d.%02d", minor/100, minor%100), nil
}

// ParseAmount 将规范金额字符串换算为分，全程整数运算，不经过浮点数。
func ParseAmount(s string) (int64, error) {
	if !amountRe.MatchString(s) {
		return 0, fmt.Errorf("marswallex: 金额格式非法: %q", s)
	}
	intPart, frac, _ := strings.Cut(s, ".")
	for len(frac) < 2 {
		frac += "0"
	}
	i, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, err
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, err
	}
	return i*100 + f, nil
}

// Timestamp 兼容秒/毫秒数字（数字或字符串）与 ISO 字符串的时间。
type Timestamp struct{ time.Time }

// 毫秒与秒的分界：>= 1e12 视为毫秒。
const msThreshold = int64(1e12)

var isoLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.000-07:00"}

func (t *Timestamp) UnmarshalJSON(b []byte) error {
	s := string(bytes.TrimSpace(b))
	if s == "null" {
		t.Time = time.Time{}
		return nil
	}
	if len(s) >= 2 && s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		s = str
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n >= msThreshold {
			t.Time = time.UnixMilli(n)
		} else {
			t.Time = time.Unix(n, 0)
		}
		return nil
	}
	for _, l := range isoLayouts {
		if v, err := time.Parse(l, s); err == nil {
			t.Time = v
			return nil
		}
	}
	return fmt.Errorf("marswallex: 无法解析时间: %q", s)
}

// MarshalJSON 输出 Unix 秒数字。
func (t Timestamp) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatInt(t.Unix(), 10)), nil
}

// Count 兼容数字与数字字符串的计数。
type Count int64

func (c *Count) UnmarshalJSON(b []byte) error {
	s := string(bytes.TrimSpace(b))
	if s == "null" {
		*c = 0
		return nil
	}
	s = strings.Trim(s, `"`)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("marswallex: 无法解析计数: %q", s)
	}
	*c = Count(n)
	return nil
}

// Page 为分页列表响应。
type Page[T any] struct {
	Count Count `json:"count"`
	Data  []T   `json:"data"`
}

// envelope 为通用的 data 包裹。
type envelope[T any] struct {
	Data T `json:"data"`
}
