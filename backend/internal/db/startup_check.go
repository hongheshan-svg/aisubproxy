package db

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

// 启动检查里要读库的部分。启动（server.New）和命令行 --check（在线更新换版本前的预检）共用，
// --check 在只读打开的库上调用，不建表、不迁移、不写任何东西。

// CheckDBPathEnv 是命令行 --check 读库路径用的专用环境变量（裁定 R17）。在线更新跑 --check 时，真实库路径只放在这里，
// DB_PATH 换成打不开的路径：不认识 --check 的老程序会把它当普通启动，按 DB_PATH 打开库那一步就失败退出，碰不到真实库。
const CheckDBPathEnv = "CDK_CHECK_DB_PATH"

// CheckPath 是 --check 要检查的库：CDK_CHECK_DB_PATH 有值用它，否则和启动一样按 ResolvePath（管理员手工运行时）。
func CheckPath() string {
	if p := strings.TrimSpace(os.Getenv(CheckDBPathEnv)); p != "" {
		return p
	}
	return ResolvePath()
}

// OpenReadOnly 只读打开已有的数据库文件（--check 用）：文件不存在时报错，不会新建；
// 连接带 mode=ro 和 query_only，任何写入都会失败。调用方负责 Close。
func OpenReadOnly(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("数据库文件 %s 不可用: %w", abs, err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("数据库路径 %s 是目录", abs)
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro&_query_only=1&_busy_timeout=10000"}
	conn, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("只读打开数据库 %s 失败: %w", abs, err)
	}
	return conn, nil
}

// CheckCardSecretsMigratable 只读判断启动时的卡台密钥加密迁移（MigrateCardSecrets）能不能做：
// 库里还有旧版明文的卡台密钥时，必须有合法的 APP_MASTER_KEY 才能加密，否则拒绝启动，不静默留着明文。
// 启动时在 MigrateCardSecrets 之前调用，命令行 --check 也调用它。
func CheckCardSecretsMigratable() error {
	for _, key := range cardSecretKeys {
		raw, err := GetSetting(key)
		if err != nil {
			return fmt.Errorf("读取 %s 失败: %w", key, err)
		}
		if !legacyPlainCardSecret(raw) {
			continue
		}
		if _, err := secretbox.LoadKey(); err != nil {
			return fmt.Errorf("库里有旧版明文的 %s 要加密迁移，需要配置合法的 APP_MASTER_KEY（base64 编码的 32 字节）: %w", key, err)
		}
	}
	return nil
}

// IsMissingSchemaErr 判断错误是不是「库里没有这张表或这一列」（SQLite 的 no such table / no such column）。
// --check 跑在旧版本的库上，用它把新版本才有的表列映射成「跳过这条检查」。
func IsMissingSchemaErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such table") || strings.Contains(msg, "no such column")
}
