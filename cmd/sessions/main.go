// Command sessions 管理本地会话，不依赖 Ollama、Milvus 或 Agent 启动。
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"my-eino-app/internal/config"
	"my-eino-app/internal/session"
)

func main() {
	deleteID := flag.String("delete", "", "删除指定 Session ID")
	cleanup := flag.Bool("cleanup", false, "删除超过 session.expire_days 的会话")
	interruptID := flag.String("interrupt", "", "确认原请求已停止后，将该会话遗留的 generating 消息标记为 interrupted")
	flag.Parse()
	cfg, err := config.Load()
	if err != nil {
		fail(err)
	}
	store, err := session.Open(cfg)
	if err != nil {
		fail(err)
	}
	defer store.Close()
	if *interruptID != "" {
		// CLI 是运维/单用户路径，没有登录态，owner 传空串（与认证关闭时的运行期一致）。
		messages, e := store.Load("", *interruptID)
		if e != nil {
			fail(e)
		}
		if len(messages) == 0 || session.MessageStatus(messages[len(messages)-1]) != "generating" {
			fail(fmt.Errorf("session has no unfinished generation"))
		}
		// 只修改最后一条消息的状态，保留原请求 ID 和已保存的部分回答。
		messages[len(messages)-1].Extra["storage_status"] = "interrupted"
		if e = store.Save("", *interruptID, messages); e != nil {
			fail(e)
		}
		fmt.Println("interrupted", *interruptID)
		return
	}
	if *deleteID != "" {
		if err := store.Delete("", *deleteID); err != nil {
			fail(err)
		}
		fmt.Println("deleted", *deleteID)
		return
	}
	if *cleanup {
		days := cfg.Session.ExpireDays
		if days <= 0 {
			days = 30
		}
		count, err := store.CleanupOlderThan(time.Duration(days) * 24 * time.Hour)
		if err != nil {
			fail(err)
		}
		fmt.Printf("cleaned %d sessions\n", count)
		return
	}
	// 列表同样按空 owner 过滤；运维清理（-cleanup）是跨 owner 的全局操作，不受影响。
	items, err := store.List("")
	if err != nil {
		fail(err)
	}
	for _, item := range items {
		fmt.Printf("%s\t%d bytes\t%s\n", item.ID, item.Size, item.ModifiedAt.Format(time.RFC3339))
	}
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
