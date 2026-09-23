# 解决登录校验失败问题

## 问题原因

之前用 `127.0.0.1` 访问过系统，浏览器保存了该域名下的认证 Cookie。
现在换成 `localhost` 访问，由于浏览器认为这是两个不同的域名，导致 Cookie 不匹配。

## 解决方法

### 方法1：清除浏览器 Cookie（推荐）

1. 在页面上按 **F12** 打开开发者工具
2. 切换到 **Application** 标签页（或 **应用程序**）
3. 左侧找到 **Cookies** → **http://localhost:18181**
4. 右键点击 **Clear** 清除所有 Cookie
5. 关闭开发者工具，刷新页面（**Ctrl+F5** 强制刷新）

### 方法2：使用隐私/无痕模式

直接在浏览器中打开新的无痕窗口（**Ctrl+Shift+N**），然后访问：
```
http://localhost:18181
```

### 方法3：关闭飞书认证（本地开发推荐）

如果只是本地开发测试，可以暂时关闭认证功能。

编辑 `.env` 文件，注释掉飞书相关配置：
```bash
# FEISHU_APP_ID=cli_aa210ce2eb78dbc9
# FEISHU_APP_SECRET=<REDACTED>
# FEISHU_REDIRECT_URL=http://localhost:18181/auth/callback
```

然后重启服务：
```bash
make restart
```

重启后直接访问 http://localhost:18181 就能看到主页面，无需登录。

---

## 验证模型选择功能

清除 Cookie 或关闭认证后，刷新页面，你应该能看到：

1. **输入框右下角**有模型选择按钮（自动路由或具体模型）
2. **点击按钮**弹出模型选择对话框
3. **对话框显示自动路由、qwen-fast 以及已配置的火山方舟模型**；Ollama 只保留 qwen3.5:9b
4. **点击切换**，发送消息时使用新模型
