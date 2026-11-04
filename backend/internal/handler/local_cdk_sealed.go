package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

// 换码换出的新码用主密钥加密存一份（local_cdks.code_sealed）：库里只有新码的哈希，客户在首页查旧码时要靠它显示新码。
// 码和密文都不进日志、审计、告警、错误信息；失败只记错误类型。

// localCDKCodeAAD 把密文绑到这一行的 code_hash 上：密文挪到别的行就解不开。
func localCDKCodeAAD(codeHash string) string { return "local_cdk_code:" + codeHash }

// sealLocalCDKCode 加密换码换出的新码。主密钥缺失或不合法、加密失败都返回空串，换码照常完成：
// 前者每天告警一次，后者只记错误类型。
func sealLocalCDKCode(code, codeHash string) string {
	key, err := secretbox.LoadKey()
	if err != nil { // LoadKey 只会报 ErrNoMasterKey、ErrBadMasterKey
		opsAlertDaily("local-cdk-code-nokey", "APP_MASTER_KEY 缺失或不合法：本站码换出的新码没有加密保存（不影响换码），客户在首页查旧码时只能看到「请联系售卖方获取」。", time.Now())
		return ""
	}
	sealed, err := secretbox.Seal(key, localCDKCodeAAD(codeHash), code)
	if err != nil {
		log.Printf("[local-cdk] 新码加密失败，没有保存（不影响换码）: %s", secretErrKind(err))
		return ""
	}
	return sealed
}

// openLocalCDKCode 取出这张码加密保存的完整码：取密文、解密、核对哈希，任何一步不成立都返回 false。
// 没有这一行、没有密文（后台批量发的码，或主密钥缺失时换的码）是正常情况，不记日志。
func openLocalCDKCode(ctx context.Context, id int64) (code string, ok bool) {
	codeHash, sealed, err := db.LocalCDKSealedCode(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", false // 客户断开或超时是正常情况，不记日志
		}
		log.Printf("[local-cdk] 码 %d 读取新码密文失败: %s", id, secretErrKind(err))
		return "", false
	}
	if sealed == "" {
		return "", false
	}
	key, err := secretbox.LoadKey()
	if err != nil {
		log.Printf("[local-cdk] 码 %d 的新码密文解不开，主密钥不可用: %s", id, secretErrKind(err))
		return "", false
	}
	plain, err := secretbox.Open(key, localCDKCodeAAD(codeHash), sealed)
	if err != nil {
		log.Printf("[local-cdk] 码 %d 的新码密文解密失败: %s", id, secretErrKind(err))
		return "", false
	}
	if db.HashCDKCode(plain) != codeHash {
		log.Printf("[local-cdk] 码 %d 解出的新码与哈希不符，不交出", id)
		return "", false
	}
	return plain, true
}

// secretErrKind 是日志里能写的错误描述：secretbox 的哨兵名，其余只写类型，不写错误内容。
func secretErrKind(err error) string {
	switch {
	case errors.Is(err, secretbox.ErrNoMasterKey):
		return "secretbox.ErrNoMasterKey"
	case errors.Is(err, secretbox.ErrBadMasterKey):
		return "secretbox.ErrBadMasterKey"
	case errors.Is(err, secretbox.ErrDecrypt):
		return "secretbox.ErrDecrypt"
	}
	return fmt.Sprintf("%T", err)
}
