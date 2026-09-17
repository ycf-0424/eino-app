# feishu-check.ps1 —— 判定飞书登录链路是否已通电（C25 前置 / 2.15 跨身份验收的前置）。
#
# 为什么需要它：飞书凭据缺失时**不会报错**。compose 读 .env 只做变量插值、不注入容器，
# 忘重建或漏透传时容器读到空值；而 auth.local.enabled=true 让 ValidateAuth 判定
# 「至少启用了一种登录方式」照常放行 —— 结果是登录页**静默少了飞书按钮**，
# 除 /health 的 login.feishu=false 外没有任何信号。肉眼看登录页会漏。
#
# 它把「跑穿」拆成四个可判定的观察点：
#   1. /health 的 login.feishu 为 true        —— 三件凭据真的进了容器
#   2. /auth/login?provider=feishu 返回 302   —— 飞书入口路由生效
#   3. 302 的 Location 参数自洽               —— client_id / scope / redirect_uri 都对
#   4. /auth/callback 对伪造 state 返回 400   —— 回调路由活着，且 state 校验真的在跑
#
# **不验的部分**（需要浏览器 + 真人授权，脚本代替不了）：
#   授权页能否打开、回跳后是否落到首页、auth_users 是否落库、owner 是否 feishu:<open_id>。
#
# 用法：
#   powershell -File scripts/feishu-check.ps1
#   powershell -File scripts/feishu-check.ps1 -BaseUrl http://127.0.0.1:18181
#   powershell -File scripts/feishu-check.ps1 -ExpectedRedirectURL ''    # 跳过第 3 项的比对
#
# 退出码：0 = 四个观察点全过；1 = 未通电（并打印最可能的成因）。

[CmdletBinding()]
param(
    # 默认对齐 .env.example 里 FEISHU_REDIRECT_URL 的宿主端口（容器映射 18180→8080）。
    [string]$BaseUrl = "http://127.0.0.1:18180",
    # 与 .env 的 FEISHU_REDIRECT_URL 比对。必须是「浏览器会用的那个主机名」，
    # 不是 BaseUrl 的等价写法 —— state 走 Cookie 按主机名隔离，localhost 与
    # 127.0.0.1 在这里不等价。传空串跳过本项断言。
    [string]$ExpectedRedirectURL = "http://localhost:18180/auth/callback",
    [int]$Timeout = 10
)

$ErrorActionPreference = "Stop"

function Write-Step([string]$m) { Write-Host "[feishu] $m" }
function Write-Pass([string]$m) { Write-Host "[feishu] OK   $m" }
function Write-Fail([string]$m) { Write-Host "[feishu] FAIL $m" }

# 从 curl -D - 的原始头里取状态码。PowerShell 5.1 没有 -SkipHttpErrorCheck，
# Invoke-WebRequest 遇到 302/400 会抛异常，拿不到头 —— 与 ratelimit-check.ps1 同理。
function Get-StatusCode($Headers) {
    $line = $Headers | Where-Object { $_ -match '^HTTP/' } | Select-Object -First 1
    if (-not $line) { return 0 }
    $parts = $line -split ' '
    if ($parts.Count -lt 2) { return 0 }
    return [int]$parts[1]
}

# 探测用哪个主机名都行（curl 眼里 localhost 与 127.0.0.1 等价），但**浏览器不行**：
# state 走 Cookie、按主机名隔离，浏览器必须用与 FEISHU_REDIRECT_URL 相同的那一个。
$browseBase = $BaseUrl
if (-not [string]::IsNullOrEmpty($ExpectedRedirectURL)) {
    $u = [System.Uri]$ExpectedRedirectURL
    $browseBase = "$($u.Scheme)://$($u.Authority)"
}

Write-Step "探测地址 $BaseUrl（仅 API 探测；浏览器请用 $browseBase）"

# ---------- 1/4 服务可达 + login 状态 ----------
$healthRaw = & curl.exe -s --max-time $Timeout "$BaseUrl/health" 2>$null
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($healthRaw)) {
    Write-Fail "1/4 GET /health 无响应。先确认服务在跑：docker ps，或 make run"
    exit 1
}

$health = $null
try { $health = $healthRaw | ConvertFrom-Json } catch {
    Write-Fail "1/4 /health 返回的不是 JSON：$healthRaw"
    exit 1
}

$feishuOn = [bool]$health.data.login.feishu
$localOn = [bool]$health.data.login.local
Write-Step "1/4 /health   login.feishu=$feishuOn   login.local=$localOn"

if (-not $feishuOn) {
    Write-Fail "飞书入口未通电：容器里的三件凭据没凑齐"
    Write-Host ""
    Write-Host "按顺序排查（前两步占九成）："
    Write-Host "  1) .env 里 FEISHU_APP_ID / FEISHU_APP_SECRET / FEISHU_REDIRECT_URL 三行是否都有值"
    Write-Host "  2) 改完 .env 后是否重建过容器（environment 只在容器创建时注入）："
    Write-Host "       docker compose -f docker-compose.milvus.yml up -d --force-recreate app"
    Write-Host "  3) 容器内实际拿到的值 —— 这一步能区分「没填」和「没透传」："
    Write-Host "       docker exec my-eino-app-app-1 env | findstr FEISHU"
    Write-Host "  4) docker-compose.milvus.yml 的 app.environment 是否还有那三行透传（不要删）"
    Write-Host ""
    if ($localOn) {
        Write-Host "提示：login.local=true，自有账号入口仍可用，系统没有失去入口（坑 B10）。"
    } else {
        Write-Host "警告：login.local 也是 false —— 当前没有任何可用的登录方式！"
    }
    exit 1
}

# ---------- 2/4 飞书入口路由 ----------
$loginHeaders = & curl.exe -s -D - -o NUL --max-time $Timeout "$BaseUrl/auth/login?provider=feishu" 2>$null
$loginStatus = Get-StatusCode $loginHeaders

if ($loginStatus -ne 302) {
    Write-Fail "2/4 GET /auth/login?provider=feishu 返回 $loginStatus，期望 302"
    $body = & curl.exe -s --max-time $Timeout "$BaseUrl/auth/login?provider=feishu" 2>$null
    if (-not [string]::IsNullOrWhiteSpace($body)) { Write-Host "      响应体：$body" }
    Write-Host "      400 且含 'feishu login is not configured' = 配置读取层的问题（回到第 1 步）"
    exit 1
}
Write-Pass "2/4 /auth/login?provider=feishu → 302"

$locLine = $loginHeaders | Where-Object { $_ -match '^(?i)location:' } | Select-Object -First 1
if (-not $locLine) {
    Write-Fail "2/4 返回 302 但没有 Location 头"
    exit 1
}
$authorizeURL = ($locLine -replace '^(?i)location:\s*', '').Trim()
Write-Step "      授权页：$authorizeURL"

$stateCookie = $loginHeaders | Where-Object { $_ -match 'eino_oauth_state' } | Select-Object -First 1
if (-not $stateCookie) {
    Write-Fail "2/4 没有种下 state Cookie（eino_oauth_state），回调时 state 校验必然失败"
    exit 1
}
Write-Pass "      已种下 state Cookie（eino_oauth_state，Path=/auth，5 分钟）"

# ---------- 3/4 授权 URL 参数自洽 ----------
Add-Type -AssemblyName System.Web
$q = [System.Web.HttpUtility]::ParseQueryString(([System.Uri]$authorizeURL).Query)

$problems = @()
if (-not $q['client_id']) { $problems += "缺少 client_id" }
if ($q['scope'] -ne 'contact:user.base:readonly') {
    $problems += "scope='$($q['scope'])'，应为 contact:user.base:readonly（internal/auth.FeishuScope 常量）"
}
if ($q['response_type'] -ne 'code') { $problems += "response_type='$($q['response_type'])'，应为 code" }
if (-not $q['state']) { $problems += "缺少 state" }
if (-not [string]::IsNullOrEmpty($ExpectedRedirectURL)) {
    if ($q['redirect_uri'] -ne $ExpectedRedirectURL) {
        $problems += "redirect_uri='$($q['redirect_uri'])' 与预期 '$ExpectedRedirectURL' 不一致（飞书侧会报 20071）"
    }
}

if ($problems.Count -gt 0) {
    Write-Fail "3/4 授权 URL 参数有问题："
    $problems | ForEach-Object { Write-Host "       - $_" }
    Write-Host "      如果只是主机名写法不同（localhost vs 127.0.0.1），用 -ExpectedRedirectURL '' 跳过本项"
    exit 1
}
Write-Pass "3/4 授权 URL 参数自洽（client_id / scope / response_type / state / redirect_uri）"

# ---------- 4/4 回调路由与 state 校验 ----------
$cbStatus = & curl.exe -s -o NUL -w "%{http_code}" --max-time $Timeout "$BaseUrl/auth/callback?state=bogus&code=bogus" 2>$null
if ("$cbStatus" -ne "400") {
    Write-Fail "4/4 /auth/callback 对伪造 state 返回 $cbStatus，期望 400 —— state 校验可能没生效"
    exit 1
}
Write-Pass "4/4 /auth/callback 拒绝伪造 state（400），回调路由活着"

Write-Host ""
Write-Pass "四个观察点全过 —— 链路已通电，剩下的只能靠浏览器"
Write-Host ""
Write-Host "接下来手工做的（脚本代替不了）："
Write-Host "  1) 浏览器打开 $browseBase/auth/login —— 应看到「飞书」按钮（配好前是静默少了它）"
Write-Host "  2) 点它并在飞书授权 —— 回跳后应落到首页，不报 state mismatch"
Write-Host "  3) 查落库：SELECT open_id, name FROM auth_users;"
Write-Host "  4) 查归属：应为 feishu:<open_id>，会话/记忆/执行记录都挂这个 owner"
Write-Host ""
Write-Host "⚠️ 浏览器必须用 $browseBase —— 不要换成另一个等价写法。"
Write-Host "   state 走 Cookie、按主机名隔离：用 127.0.0.1 打开登录页、回调却跳到 localhost，"
Write-Host "   Cookie 落不到同一个域 → 400 state mismatch，且现象看起来像「飞书后台配错了」。"
exit 0
