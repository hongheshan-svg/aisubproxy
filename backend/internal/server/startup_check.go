package server

import (
	"fmt"

	"github.com/tuzi/cdk-recharge-system/internal/config"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/handler"
)

// startupChecks 是会让服务拒绝启动的检查，只读数据库和环境变量、不改任何东西。
// 启动时（New，建好表之后、改数据之前）和命令行 --check（在线更新换版本前的预检，只读打开数据库）
// 共用这一个函数：两边的规则只有这一份，--check 通过，新版本就不会因为这些条件起不来（审计 auth N-2）。
func startupChecks(mode string) error {
	// 首次启动要按 ADMIN_PASSWORD 建管理员时，口令太弱就拒绝启动（db.Init 建号用的是同一个判定）
	if err := db.CheckDefaultAdmin(); err != nil {
		return err
	}
	// 卡台 API Key、webhook 密钥：库里还有旧版明文时必须能加密迁移，否则不启动，不静默留着明文
	if err := db.CheckCardSecretsMigratable(); err != nil {
		return fmt.Errorf("卡台密钥迁移: %w", err)
	}
	// 生产模式、或已配置动钱上游密钥时，强制要求安全的 JWT secret，避免 token 被伪造后调用动钱接口
	upstream, err := upstreamKeysConfigured()
	if err != nil {
		return err
	}
	if err := enforceJWTSecret(mode, upstream); err != nil {
		return err
	}
	// 每家注册了的服务商都要接上 handler 层的适配器，否则它的码会被分派过来却没人接
	return handler.CheckProviderAdapters()
}

// Check 是命令行 --check：只读打开现有数据库，按当前环境变量跑一遍 startupChecks。
// 不建表、不迁移、不写库，不连任何外部服务，不启动后台任务。返回 nil 表示这个版本能通过启动检查。
// 数据库文件不存在时报错（--check 只检查已有的安装）。
func Check(cfg *config.Config) error {
	conn, err := db.OpenReadOnly(db.ResolvePath())
	if err != nil {
		return err
	}
	old := db.DB
	db.DB = conn
	defer func() {
		db.DB = old
		conn.Close()
	}()
	return startupChecks(cfg.Server.Mode)
}
