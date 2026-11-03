package db

// 只给外部测试包用的入口：db_test 引用 dbtest，dbtest 又引用 db，所以测试只能放在外部包。
var (
	EncryptLegacyCDKSessionsN = encryptLegacyCDKSessions
	ClearCDKSessionRowIf      = clearCDKSessionRowIf
)
