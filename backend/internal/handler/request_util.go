package handler

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
)

func deviceFrom(c *gin.Context) string {
	if d := strings.TrimSpace(c.GetHeader("X-Redemption-Device")); d != "" {
		return d
	}
	return c.GetHeader("User-Agent")
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(toString(v))
}

func toString(v any) string {
	b, _ := json.Marshal(v)
	return strings.Trim(string(b), `"`)
}
