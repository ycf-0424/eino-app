// Command boundary 校验 eino 编排 API 未扩散到 internal/eino 之外。
//
// 为什么需要这道守卫（EXECUTION-PLAN 步骤 3.2）：把 7 个包收进 internal/eino
// 只解决了当下的依赖位置，新代码很容易再把 compose/adk/callbacks 直接 import
// 回业务包，几个月就扩散回去。守卫的作用是让扩散当场失败，而不是靠人记得。
//
// 为什么用 Go 而不是方案给的 grep 管道：本项目的 make 在 Windows 上走 cmd.exe，
// `@! grep -rl ... | grep -v ... || (echo ... && exit 1)` 这套 POSIX 写法在那里
// 直接报 “grep 不是内部或外部命令”。Go 版本跨平台、零外部依赖，且能被单元测试覆盖。
package main

import (
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// guarded 是被守卫的 eino 编排 API：只允许出现在 internal/eino/ 内。
// components/* 与 schema 不在守卫范围内 —— 它们是数据与组件接口，业务包按
// 步骤 3.3 的门面约定仍需少量使用。
var guarded = []string{
	"github.com/cloudwego/eino/compose",
	"github.com/cloudwego/eino/adk",
	"github.com/cloudwego/eino/callbacks",
}

// allowedDir 是唯一允许 import 上述依赖的目录（相对仓库根，用 / 分隔）。
const allowedDir = "internal/eino"

func main() {
	root := flag.String("root", ".", "仓库根目录")
	flag.Parse()

	violations, scanned, err := scan(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(violations) > 0 {
		for _, item := range violations {
			fmt.Fprintln(os.Stderr, item)
		}
		fmt.Fprintf(os.Stderr, "❌ compose/adk/callbacks 出现在 %s/ 之外，共 %d 处\n", allowedDir, len(violations))
		os.Exit(1)
	}
	fmt.Printf("✅ eino 编排边界完好（扫描 %d 个 .go 文件）\n", scanned)
}

// scan 返回越界 import 的 “文件:行: import "路径"” 描述与扫描到的文件数。
// 只看 import 声明（parser.ImportsOnly），所以注释或字符串里提到这些包不会误报。
func scan(root string) ([]string, int, error) {
	var violations []string
	scanned := 0
	fset := token.NewFileSet()
	for _, dir := range []string{"internal", "cmd"} {
		base := filepath.Join(root, dir)
		// 目录不存在就跳过：单包仓库、裁剪过的目录树或测试用的临时目录
		// 不该让守卫直接报错 —— 守卫的职责是拦越界 import，不是校验目录布局。
		if info, err := os.Stat(base); err != nil || !info.IsDir() {
			continue
		}
		walkErr := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if rel == allowedDir || strings.HasPrefix(rel, allowedDir+"/") {
				return nil
			}
			scanned++
			file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return fmt.Errorf("parse %s: %w", rel, err)
			}
			for _, spec := range file.Imports {
				value, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					continue
				}
				for _, prefix := range guarded {
					if value == prefix || strings.HasPrefix(value, prefix+"/") {
						violations = append(violations,
							fmt.Sprintf("%s:%d: import %q", rel, fset.Position(spec.Pos()).Line, value))
					}
				}
			}
			return nil
		})
		if walkErr != nil {
			return nil, 0, walkErr
		}
	}
	return violations, scanned, nil
}
