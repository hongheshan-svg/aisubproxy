package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/config"
	"github.com/tuzi/cdk-recharge-system/internal/handler"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
	"github.com/tuzi/cdk-recharge-system/internal/server"
)

func main() {
	// 命令行子命令在加载配置、连数据库之前处理：在线更新的自检会运行 `--version` 和 `--check`，
	// 新版本起不来时用 `rollback` 恢复上一个版本。
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "version":
			fmt.Println(handler.LocalVersion())
			return
		case "--check":
			// 启动检查：按当前环境变量和数据库（只读打开）跑一遍启动时会拒绝启动的检查，和启动用同一个函数。
			// 不建表、不迁移、不写库，不启动任何后台任务。在线更新换文件之前用它确认新版本起得来。
			// 库路径读 CDK_CHECK_DB_PATH（在线更新时 DB_PATH 被故意换成打不开的路径），没设就和启动一样读 DB_PATH。
			cfg, err := config.Load()
			if err == nil {
				err = server.Check(cfg)
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, handler.CheckFailPrefix+":", err)
				os.Exit(1)
			}
			fmt.Println(handler.CheckOKLine)
			return
		case "rollback":
			if err := handler.Rollback(os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, "回滚失败:", err)
				os.Exit(1)
			}
			return
		}
	}

	// Docker 部署：数据卷里有更新的在线升级版本时换过去运行（exec 成功则不返回）。
	// 必须在加载配置、打开数据库之前；原生部署什么也不做。
	handler.RuntimeHandoff()

	// 收到 SIGTERM/SIGINT 时先排空在途付款单再退出（见下面的 Shutdown）。后台协程用单独的 runCtx：
	// 信号不能直接取消它，否则正在开浏览器、注资、填表的单会被当场打断。
	sigCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stopSignals()
	runCtx, stopRun := context.WithCancel(context.Background())
	defer stopRun()

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Set Gin mode
	if cfg.Server.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	} else {
		gin.SetMode(gin.DebugMode)
	}

	// Initialize server
	srv, err := server.New(runCtx, cfg)
	if err != nil {
		log.Fatalf("Failed to initialize server: %v", err)
	}

	// Start server：先监听，确认端口开了再提供服务
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("Starting server on %s", addr)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Server error: %v", err)
	}
	// 数据库已打开、HTTP 已开始监听：在线升级的新版本算启动成功，清零启动次数；退回过镜像版本的在这里告警
	handler.RuntimeBootCompleted()
	// 老版本留下的升级备份按 umask 落盘，启动时收紧到只有本用户能读
	handler.TightenUpdateBackups()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		if err != nil {
			log.Fatalf("Server error: %v", err)
		}
		return
	case <-sigCtx.Done():
	}
	// 恢复默认的信号处理：等待期间再按一次 Ctrl+C（或 systemd/docker 到时限发 SIGKILL）就立即退出
	stopSignals()
	log.Printf("收到停止信号：停止派发新付款单，等在途单进入确认或结束（最多 %v），然后关闭 HTTP", payengine.DrainMax)
	if err := srv.Shutdown(context.Background()); err != nil {
		log.Printf("关闭 HTTP 出错: %v", err)
	}
	stopRun()
	log.Printf("服务已停止")
}
