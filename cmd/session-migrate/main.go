// Command session-migrate 显式建表或导入历史会话，不依赖模型和向量数据库。
package main

import (
	"flag"
	"fmt"
	"my-eino-app/internal/config"
	"my-eino-app/internal/session"
	"os"
	"strings"
)

func main() {
	source := flag.String("source", "", "历史 JSON 目录；留空只建表")
	dry := flag.Bool("dry-run", false, "只解析并统计源文件，不写数据库")
	indexes := flag.Bool("memory-indexes", false, "为已有记忆表补充清理索引（需要 ALTER 权限）")
	claimOwner := flag.String("claim-owner", "", "把 owner_id 为空的历史会话归属给该 owner（如 feishu:ou_xxx）；留空则不改动")
	flag.Parse()
	cfg, err := config.Load()
	if err != nil {
		fail(err)
	}
	// 迁移需要 CREATE / ALTER / INDEX 权限，而应用账号按设计只有 DML。
	// 配置 MYSQL_MIGRATE_USER / MYSQL_MIGRATE_PASSWORD 时，本命令改用该特权账号；
	// 未配置则回落到应用账号，适用于应用账号本身持有 DDL 权限的部署。
	// 只影响本命令，应用进程的连接凭据不受影响。
	if user := strings.TrimSpace(os.Getenv("MYSQL_MIGRATE_USER")); user != "" {
		cfg.MySQL.User = user
		if password, ok := os.LookupEnv("MYSQL_MIGRATE_PASSWORD"); ok {
			cfg.MySQL.Password = password
		}
		fmt.Printf("migrate account=%s\n", user)
	}
	var src *session.Store
	var items []session.Info
	if *source != "" {
		info, e := os.Stat(*source)
		if e != nil {
			fail(e)
		}
		if !info.IsDir() {
			fail(fmt.Errorf("source must be a directory"))
		}
		src, err = session.New(*source)
		if err != nil {
			fail(err)
		}
		// 源目录是文件后端，没有 owner 维度，传空串。
		items, err = src.List("")
		if err != nil {
			fail(err)
		}
	}
	var dst *session.Store
	if !*dry {
		cfg.Session.Store = "mysql"
		dst, err = session.Open(cfg)
		if err != nil {
			fail(err)
		}
		defer dst.Close()
		// Only migrate schemas used by the active configuration. In particular,
		// an application account with DML-only grants must not fail because the
		// optional execution-events schema is disabled and absent.
		// IncludeAuth 例外地无条件为 true：conversations.owner_id 是会话表本身的
		// 结构，不是可关掉的子系统，缺少它会让接入认证后的会话读写直接失败。
		if err = dst.MigrateWithOptions(session.MigrationOptions{
			IncludeExecution:     cfg.ExecutionEvents.Enabled,
			IncludeMemory:        cfg.Memory.Enabled,
			IncludeMemoryIndexes: *indexes,
			IncludeAuth:          true,
		}); err != nil {
			fail(err)
		}
		if *claimOwner != "" {
			claimed, e := dst.ClaimLegacySessions(*claimOwner)
			if e != nil {
				fail(e)
			}
			fmt.Printf("claimed legacy sessions=%d owner=%s\n", claimed, *claimOwner)
		}
	}
	count := 0
	for _, item := range items {
		messages, e := src.Load("", item.ID)
		if e != nil {
			fail(e)
		}
		if !*dry {
			// 导入的会话先写成「无归属」，再由 -claim-owner 一次性归属给指定 owner；
			// 这正是历史数据迁移的预期路径，不要在这里猜 owner。
			if e = dst.Save("", item.ID, messages); e != nil {
				fail(fmt.Errorf("import %s: %w", item.ID, e))
			}
		}
		count += len(messages)
	}
	fmt.Printf("sessions=%d messages=%d dry_run=%v\n", len(items), count, *dry)
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
