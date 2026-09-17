// Command user-admin 管理项目自有账号（建号、重设口令、停用/启用、列出）。
//
// 为什么建号走命令行而不是 HTTP（D15）：这是「仅管理员创建」的注册策略下最省的实现
// —— 没有注册接口，就没有刷号风险，也不需要一套管理端鉴权与页面。第一个管理员也只能
// 这样建出来（管理端 API 方案的第一步就卡在「谁来创建第一个管理员」）。
//
// 用法：
//
//	go run ./cmd/user-admin -create -username=admin -password=强口令 -admin
//	go run ./cmd/user-admin -passwd -username=admin            # 口令从标准输入读取
//	go run ./cmd/user-admin -disable -username=admin
//	go run ./cmd/user-admin -list
package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"my-eino-app/internal/auth"
	"my-eino-app/internal/config"
	"my-eino-app/internal/session"
)

func main() {
	create := flag.Bool("create", false, "创建账号")
	passwd := flag.Bool("passwd", false, "重设口令（忘记密码的解法）")
	disable := flag.Bool("disable", false, "停用账号")
	enable := flag.Bool("enable", false, "启用账号")
	list := flag.Bool("list", false, "列出账号（不输出口令哈希）")
	username := flag.String("username", "", "账号名：3-32 位字母、数字或下划线")
	password := flag.String("password", "", "口令；留空则从标准输入读取（避免残留在 shell 历史里）")
	admin := flag.Bool("admin", false, "创建为管理员")
	displayName := flag.String("name", "", "显示名；留空使用账号名")
	flag.Parse()

	actions := map[string]bool{"-create": *create, "-passwd": *passwd, "-disable": *disable, "-enable": *enable, "-list": *list}
	chosen := ""
	for name, on := range actions {
		if !on {
			continue
		}
		if chosen != "" {
			fmt.Fprintf(os.Stderr, "只能指定一个操作，同时给了 %s 与 %s\n", chosen, name)
			os.Exit(2)
		}
		chosen = name
	}
	if chosen == "" {
		flag.Usage()
		os.Exit(2)
	}

	// -list 不需要账号名；其余操作必须给 -username。
	if chosen != "-list" && strings.TrimSpace(*username) == "" {
		fmt.Fprintf(os.Stderr, "%s 需要 -username\n", chosen)
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		fail(fmt.Errorf("config: %w", err))
	}
	// 账号只存在于 MySQL；配置里写成 file 时也要按 mysql 连，避免静默操作到文件后端。
	cfg.Session.Store = "mysql"
	store, err := session.Open(cfg)
	if err != nil {
		fail(err)
	}
	defer store.Close()
	db := store.DB()
	if db == nil {
		fail(errors.New("需要 SESSION_STORE=mysql 才能管理自有账号"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := ensureSchema(ctx, db); err != nil {
		fail(err)
	}

	accounts := auth.NewLocalStore(db)
	minLength := cfg.Auth.Local.MinPasswordLength

	switch chosen {
	case "-list":
		items, err := accounts.List(ctx)
		if err != nil {
			fail(err)
		}
		if len(items) == 0 {
			fmt.Println("没有账号。可用 -create -username=<u> -password=<p> [-admin] 建号。")
			return
		}
		writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "USERNAME\tDISPLAY\tADMIN\tDISABLED\tCREATED\tLAST SEEN")
		for _, item := range items {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
				item.Username, item.DisplayName, yesNo(item.IsAdmin), yesNo(item.Disabled),
				item.CreatedAt.Format(time.DateTime), item.LastSeenAt.Format(time.DateTime))
		}
		if err := writer.Flush(); err != nil {
			fail(err)
		}
	case "-create":
		secret, err := resolvePassword(bufio.NewReader(os.Stdin), *password)
		if err != nil {
			fail(err)
		}
		user, err := accounts.Create(ctx, *username, secret, *displayName, *admin, minLength)
		if err != nil {
			fail(err)
		}
		fmt.Printf("已创建账号 username=%s id=%s admin=%v\n", user.Username, user.ID, user.IsAdmin)
	case "-passwd":
		secret, err := resolvePassword(bufio.NewReader(os.Stdin), *password)
		if err != nil {
			fail(err)
		}
		if err := accounts.SetPassword(ctx, *username, secret, minLength); err != nil {
			fail(err)
		}
		fmt.Printf("已重设口令 username=%s\n", *username)
	case "-disable", "-enable":
		disabled := chosen == "-disable"
		if err := accounts.SetDisabled(ctx, *username, disabled); err != nil {
			fail(err)
		}
		fmt.Printf("已%s账号 username=%s\n", map[bool]string{true: "停用", false: "启用"}[disabled], *username)
	}
}

// resolvePassword 取口令：优先用 -password，缺省时从标准输入读取并要求二次确认。
//
// 口令不走命令行参数是刻意设计：参数会留在 shell 历史里，也可能出现在进程列表里。
// reader 作为参数传入，便于在测试里验证「两次输入不一致」这类分支。
func resolvePassword(reader *bufio.Reader, fromFlag string) (string, error) {
	if strings.TrimSpace(fromFlag) != "" {
		return fromFlag, nil
	}
	fmt.Fprintln(os.Stderr, "提示：标准输入无法关闭回显，口令会明文显示，注意周围环境。")
	first, err := readLine(reader, "口令：")
	if err != nil {
		return "", err
	}
	second, err := readLine(reader, "再次输入以确认：")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("两次输入的口令不一致")
	}
	return first, nil
}

func readLine(reader *bufio.Reader, prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("读取口令失败: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// ensureSchema 在建号前确认表存在，把「忘了迁移」变成一句明确的提示，
// 而不是一句 MySQL 的 "Table doesn't exist"。
func ensureSchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, "SELECT 1 FROM auth_local_users LIMIT 0"); err != nil {
		return fmt.Errorf("auth_local_users 尚不可用，请先执行 make db-migrate: %w", err)
	}
	return nil
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
