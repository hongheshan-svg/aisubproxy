package db

import "github.com/tuzi/cdk-recharge-system/internal/secretbox"

// 只给外部测试包用的入口：db_test 引用 dbtest，dbtest 又引用 db，所以测试只能放在外部包。
var (
	EncryptLegacyCDKSessionsN = encryptLegacyCDKSessions
	ClearCDKSessionRowIf      = clearCDKSessionRowIf
)

// OpenStoredSession 测试用：把库里的会话值还原成原文（生产代码已不再读取旧绑定会话，只测加密迁移的结果）。
// 未迁移的明文原样返回。
func OpenStoredSession(stored string) (string, error) {
	if stored == "" || !isSealedSession(stored) {
		return stored, nil
	}
	key, err := secretbox.LoadKey()
	if err != nil {
		return "", err
	}
	return secretbox.Open(key, cdkSessionAAD, stored)
}
