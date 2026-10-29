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

	KindConversion:     "MC",
	KindWalletTransfer: "MT",
}

// newOutOrderNo 生成商户单号，前缀按单据类型。
func newOutOrderNo(kind string, now time.Time) string {
	return newOutID(orderPrefix[kind], now)
}

// newOutID 生成商户侧 ID：两位前缀 + UTC yymmddHHMMSS + 8 位小写十六进制，共 22 位。
func newOutID(prefix string, now time.Time) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + now.UTC().Format("060102150405") + hex.EncodeToString(b[:])
}
