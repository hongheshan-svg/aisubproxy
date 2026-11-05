package acgshared

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"sort"
	"strings"
)

// Sign 按协议计算签名：去掉顶层 sign，顶层键按字节序排序，丢弃值为空串的顶层标量
// （嵌套里的空串保留），标量写 键=值，嵌套逐项写 名[子键]=值，& 连接后加 &key=密钥，md5 小写。
func Sign(f Form, key string) string {
	fields := make([]Field, 0, len(f.Fields))
	for _, fd := range f.Fields {
		if fd.Key == "sign" {
			continue
		}
		fields = append(fields, fd)
	}
	sort.SliceStable(fields, func(i, j int) bool { return fields[i].Key < fields[j].Key })
	var parts []string
	for _, fd := range fields {
		if fd.Sub != nil {
			for _, s := range fd.Sub {
				parts = append(parts, fd.Key+"["+s.Key+"]="+s.Value)
			}
			continue
		}
		if fd.Value == "" {
			continue
		}
		parts = append(parts, fd.Key+"="+fd.Value)
	}
	parts = append(parts, "key="+key)
	sum := md5.Sum([]byte(strings.Join(parts, "&")))
	return hex.EncodeToString(sum[:])
}

// Verify 校验标量 sign；缺失或是嵌套返回 false。
func Verify(f Form, key string) bool {
	got, ok := f.Get("sign")
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(Sign(f, key))) == 1
}
