package server

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 前端是手写、无构建的三件套：app.js 用 document.getElementById 取元素，
// id 写错不会报错，只会在运行到 ui.xxx.hidden 时抛异常（例如用户区）。
// 这里把「app.js 引用的 id」与「index.html 声明的 id」对齐校验，作为回归网。
func TestWebAssetsElementIDsMatch(t *testing.T) {
	html, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := os.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}

	declared := map[string]struct{}{}
	for _, match := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(string(html), -1) {
		declared[match[1]] = struct{}{}
	}

	// 列表元素全是字符串字面量，取到第一个 ']' 即可（元素内不含 ']'）。
	block := regexp.MustCompile(`(?s)const ui = Object\.fromEntries\(\[(.*?)\]`).FindStringSubmatch(string(js))
	if block == nil {
		t.Fatal("app.js 中未找到 ui 元素列表")
	}
	used := regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(block[1], -1)
	if len(used) == 0 {
		t.Fatal("app.js 的 ui 元素列表为空")
	}
	for _, match := range used {
		if _, ok := declared[match[1]]; !ok {
			t.Errorf("app.js 引用了 index.html 中不存在的元素 id %q", match[1])
		}
	}

	// 登录态区域的 id 是步骤 2.9/2.14 的接口点，单独钉住。
	for _, id := range []string{"userBox", "userName", "logoutButton"} {
		if _, ok := declared[id]; !ok {
			t.Errorf("index.html 缺少登录态元素 id=%q", id)
		}
	}
	if !strings.Contains(string(js), `ui.logoutButton.onclick`) {
		t.Error("app.js 未绑定登出按钮")
	}
}

// 登录页的入口块靠成对的注释标记裁剪（renderLoginPage 的 stripMarkedBlock）：
// 标记写错会导致「未启用的入口仍然显示」或者整块被吞掉，且不会报错。
func TestLoginPageProviderMarkersAreBalanced(t *testing.T) {
	page, err := os.ReadFile("web/login.html")
	if err != nil {
		t.Fatal(err)
	}
	body := string(page)
	for _, name := range []string{"provider:feishu", "provider:local", "both:divider"} {
		opening := strings.Count(body, "<!--"+name+"-->")
		closing := strings.Count(body, "<!--/"+name+"-->")
		if opening != 1 || closing != 1 {
			t.Errorf("标记 %s 出现次数 open=%d close=%d，应为 1/1", name, opening, closing)
		}
	}
	// 表单必须有提交去处，且页面不能出现注册入口（D12）。
	if !strings.Contains(body, `"/auth/local"`) {
		t.Error("login.html 未引用 /auth/local")
	}
	if strings.Contains(body, "注册") {
		t.Error("login.html 出现了注册相关字样")
	}
}
