// Package acgshared 实现 acg-faka 店铺共享协议里的纯函数：表单解析、签名、金额、商品对象和发货文本。
package acgshared

import (
	"errors"
	"net/url"
	"strings"
)

// SubField 是嵌套字段（如 sku[颜色]=红）里的一项。
type SubField struct{ Key, Value string }

// Field 是顶层字段。Sub != nil 时是嵌套数组，Value 不用。
type Field struct {
	Key   string
	Value string
	Sub   []SubField
}

// Form 保留顶层字段第一次出现的顺序；同名顶层标量以最后一个为准。
type Form struct{ Fields []Field }

// ErrBadForm 表示请求体不是合法的表单。
var ErrBadForm = errors.New("请求格式不正确")

// ParseForm 解析 application/x-www-form-urlencoded，规则是 PHP parse_str 的子集。
func ParseForm(body []byte) (Form, error) {
	var f Form
	index := map[string]int{}
	for _, seg := range strings.Split(string(body), "&") {
		if seg == "" {
			continue
		}
		rawKey, rawVal, _ := strings.Cut(seg, "=")
		key, err := url.QueryUnescape(rawKey)
		if err != nil {
			return Form{}, ErrBadForm
		}
		val, err := url.QueryUnescape(rawVal)
		if err != nil {
			return Form{}, ErrBadForm
		}
		name, sub, nested, ok := splitKey(key)
		if !ok {
			return Form{}, ErrBadForm
		}
		i, seen := index[name]
		if !seen {
			index[name] = len(f.Fields)
			if nested {
				f.Fields = append(f.Fields, Field{Key: name, Sub: []SubField{{sub, val}}})
			} else {
				f.Fields = append(f.Fields, Field{Key: name, Value: val})
			}
			continue
		}
		fd := &f.Fields[i]
		if nested != (fd.Sub != nil) {
			return Form{}, ErrBadForm
		}
		if !nested {
			fd.Value = val
			continue
		}
		replaced := false
		for j := range fd.Sub {
			if fd.Sub[j].Key == sub {
				fd.Sub[j].Value = val
				replaced = true
				break
			}
		}
		if !replaced {
			fd.Sub = append(fd.Sub, SubField{sub, val})
		}
	}
	return f, nil
}

// splitKey 拆 `名` 或 `名[子键]`；其他任何 [ ] 用法都不合法。
func splitKey(key string) (name, sub string, nested, ok bool) {
	i := strings.IndexAny(key, "[]")
	if i < 0 {
		return key, "", false, true
	}
	if key[i] != '[' || i == 0 || !strings.HasSuffix(key, "]") {
		return "", "", false, false
	}
	name = key[:i]
	sub = key[i+1 : len(key)-1]
	if sub == "" || strings.ContainsAny(sub, "[]") {
		return "", "", false, false
	}
	return name, sub, true, true
}

// Get 只取标量；嵌套或不存在返回 "", false。
func (f Form) Get(key string) (string, bool) {
	for _, fd := range f.Fields {
		if fd.Key == key && fd.Sub == nil {
			return fd.Value, true
		}
	}
	return "", false
}

// Has 标量或嵌套都算。
func (f Form) Has(key string) bool {
	for _, fd := range f.Fields {
		if fd.Key == key {
			return true
		}
	}
	return false
}
