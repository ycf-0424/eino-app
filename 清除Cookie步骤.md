# 清除浏览器 Cookie 的步骤

服务已重启，飞书配置已正确设置为 `http://localhost:18181/auth/callback`。

现在**必须清除浏览器的旧 Cookie**，否则还会报 `state mismatch` 错误。

## 清除方法（选一种）

### 方法1：开发者工具清除（推荐）

1. 在浏览器中打开 http://localhost:18181
2. 按 **F12** 打开开发者工具
3. 点击顶部的 **Application** 标签（或 **应用程序**）
4. 左侧展开 **Cookies** → 点击 **http://localhost:18181**
5. 在右侧 Cookie 列表中，找到所有 Cookie（特别是 `state` 相关的）
6. 右键点击列表空白处，选择 **Clear all** 或 **清除所有**
7. 关闭开发者工具
8. **强制刷新页面**：按 **Ctrl + Shift + R** 或 **Ctrl + F5**

### 方法2：无痕窗口（最简单）

1. 在浏览器中打开新的无痕/隐私窗口
   - Chrome/Edge: **Ctrl + Shift + N**
   - Firefox: **Ctrl + Shift + P**
2. 在无痕窗口中访问：http://localhost:18181
3. 点击「登录」进行飞书授权

### 方法3：浏览器设置清除

1. 浏览器设置 → 隐私和安全 → 清除浏览数据
2. 只选中 **Cookie 和其他网站数据**
3. 时间范围选 **所有时间** 或 **过去1小时**
4. 点击清除
5. 访问 http://localhost:18181

---

## 清除后的验证步骤

1. 访问 http://localhost:18181
2. 点击「登录」按钮
3. 跳转到飞书授权页面
4. 授权后应该能正常登录，不会再报 `state mismatch`

---

## 如果还是失败

按 F12 打开开发者工具，切换到 **Console** 标签，把红色错误信息截图发给我。
