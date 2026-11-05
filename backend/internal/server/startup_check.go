package server

import (
	"fmt"
	"log"

	"github.com/tuzi/cdk-recharge-system/internal/config"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/handler"
)

// startupChecks 是会让服务拒绝启动的检查，只读数据库和环境变量、不改任何东西。
// 启动时（New，建好表之后、改数据之前）和命令行 --check（在线更新换版本前的预检，只读打开数据库）
// 共用这一个函数：两边的规则只有这一份，--check 通过，新版本就不会因为这些条件起不来（审计 auth N-2）。
//
// 注意（审计 N-3）：--check 用新版本代码跑在旧版本的库上，库里只有上一个正式版建过的表和列。
// 所以这里的检查只能依赖上一个正式版已经有的表和列（目前是 admin_users、site_settings）；
// 要读新表、新列，必须先判断它存在，不能直接查。tolerateMissingSchema 为 true（只有 --check）时，
// 一条检查撞上 no such table / no such column 就跳过那一条并打印一行说明，不算失败；
// 正式启动时表一定已建好，缺表照常当错误，不会被掩盖。
func startupChecks(mode string, tolerateMissingSchema bool) error {
	// step 跑一条检查；返回 skipped 表示因为旧库缺表缺列而跳过。
	step := func(name string, fn func() error) (skipped bool, err error) {
		err = fn()
		if err != nil && tolerateMissingSchema && db.IsMissingSchemaErr(err) {
			log.Printf("--check：%s 需要的表或列在当前数据库里还没有（%v），跳过这一条", name, err)
			return true, nil
		}
		return false, err
	}
	// 首次启动要按 ADMIN_PASSWORD 建管理员时，口令太弱就拒绝启动（db.Init 建号用的是同一个判定）
	if _, err := step("默认管理员口令检查", db.CheckDefaultAdmin); err != nil {
		return err
	}
	// 卡台 API Key、webhook 密钥：库里还有旧版明文时必须能加密迁移，否则不启动，不静默留着明文
	if _, err := step("卡台密钥迁移检查", func() error {
		if err := db.CheckCardSecretsMigratable(); err != nil {
			return fmt.Errorf("卡台密钥迁移: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	// 生产模式、或已配置动钱上游密钥时，强制要求安全的 JWT secret，避免 token 被伪造后调用动钱接口
	// 读不到上游密钥（表还不存在）就等于没配过，按未配置处理；release 模式照旧一律要求。
	upstream := false
	if _, err := step("上游密钥检查", func() (e error) {
		upstream, e = upstreamKeysConfigured()
		return e
	}); err != nil {
		return err
	}
	if err := enforceJWTSecret(mode, upstream); err != nil {
		return err
	}
	// 每家注册了的服务商都要接上 handler 层的适配器，否则它的码会被分派过来却没人接
	return handler.CheckProviderAdapters()
}

// Check 是命令行 --check：只读打开现有数据库，按当前环境变量跑一遍 startupChecks。
// 不建表、不迁移、不写库，不连任何外部服务，不启动后台任务。库缺表缺列的检查会跳过（见 startupChecks）。返回 nil 表示这个版本能通过启动检查。
// 库路径用 db.CheckPath（专用变量 CDK_CHECK_DB_PATH 优先）；数据库文件不存在时报错（--check 只检查已有的安装）。
func Check(cfg *config.Config) error {
	conn, err := db.OpenReadOnly(db.CheckPath())
	if err != nil {
		return err
	}
	old := db.DB
	db.DB = conn
	defer func() {
		db.DB = old
		conn.Close()
	}()
	return startupChecks(cfg.Server.Mode, true)
}
