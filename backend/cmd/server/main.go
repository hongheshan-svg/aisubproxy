package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/config"
	"github.com/tuzi/cdk-recharge-system/internal/handler"
	"github.com/tuzi/cdk-recharge-system/internal/server"
)

func main() {
	// 命令行子命令在加载配置、连数据库之前处理：在线更新的自检会运行 `--version`，
	// 新版本起不来时用 `rollback` 恢复上一个版本。
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "version":
			fmt.Println(handler.LocalVersion())
			return
		case "rollback":
			if err := handler.Rollback(os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, "回滚失败:", err)
				os.Exit(1)
			}
			return
		}
	}

	ctx := context.Background()

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
	srv, err := server.New(ctx, cfg)
	if err != nil {
		log.Fatalf("Failed to initialize server: %v", err)
	}

	// Start server
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("Starting server on %s", addr)
	if err := srv.Run(addr); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
