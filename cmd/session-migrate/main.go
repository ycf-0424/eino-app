// Command session-migrate 显式建表或导入历史会话，不依赖模型和向量数据库。
package main

import (
	"flag"
	"fmt"
	"my-eino-app/internal/config"
	"my-eino-app/internal/session"
	"os"
)

func main() {
	source := flag.String("source", "", "历史 JSON 目录；留空只建表")
	dry := flag.Bool("dry-run", false, "只解析并统计源文件，不写数据库")
	indexes := flag.Bool("memory-indexes", false, "为已有记忆表补充清理索引（需要 ALTER 权限）")
	flag.Parse()
	cfg, err := config.Load()
	if err != nil {
		fail(err)
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
		items, err = src.List()
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
		if err = dst.MigrateWithOptions(session.MigrationOptions{
			IncludeExecution:     cfg.ExecutionEvents.Enabled,
			IncludeMemory:        cfg.Memory.Enabled,
			IncludeMemoryIndexes: *indexes,
		}); err != nil {
			fail(err)
		}
	}
	count := 0
	for _, item := range items {
		messages, e := src.Load(item.ID)
		if e != nil {
			fail(e)
		}
		if !*dry {
			// 相同 ID 必须整段相同，或现有记录是源文件的前缀；冲突拒绝覆盖。
			if e = dst.Save(item.ID, messages); e != nil {
				fail(fmt.Errorf("import %s: %w", item.ID, e))
			}
		}
		count += len(messages)
	}
	fmt.Printf("sessions=%d messages=%d dry_run=%v\n", len(items), count, *dry)
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
