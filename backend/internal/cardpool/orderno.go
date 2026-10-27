package cardpool

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

var orderPrefix = map[string]string{
	KindApply:    "MA",
	KindRecharge: "MI",
	KindWithdraw: "MO",
	KindStatus:   "MS",
}

// newOutOrderNo 生成商户单号：两位前缀 + UTC yymmddHHMMSS + 8 位小写十六进制，共 22 位。
func newOutOrderNo(kind string, now time.Time) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return orderPrefix[kind] + now.UTC().Format("060102150405") + hex.EncodeToString(b[:])
}
