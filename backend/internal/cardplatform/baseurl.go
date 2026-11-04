package cardplatform

import (
	"errors"
	"net/url"
	"strings"
)

// NormalizeBase 与 LoadConfig 对 card_api_base 的整理一致：去空白、尾斜杠、/openapi(/v1) 后缀；空值用默认地址。
func NormalizeBase(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	base = strings.TrimSuffix(base, "/openapi/v1")
	base = strings.TrimSuffix(base, "/openapi")
	if base == "" {
		base = defaultBase
	}
	return base
}

// ValidateBase 校验后台保存的卡台地址：只接受 https，本机回环地址（127.0.0.1、localhost、::1）可用 http；
// 不得带用户信息、查询串、片段。防止把 API Key 发到明文或别人的地址。
func ValidateBase(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return errors.New("卡台地址无法解析")
	}
	if u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return errors.New("卡台地址不得包含用户信息、查询串或片段")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return errors.New("卡台地址缺少主机名")
	}
	loopback := host == "127.0.0.1" || host == "localhost" || host == "::1"
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && loopback:
	default:
		return errors.New("卡台地址必须使用 https（本机调试地址除外）")
	}
	if strings.ContainsAny(u.Path, " \t\r\n") {
		return errors.New("卡台地址包含非法字符")
	}
	return nil
}
