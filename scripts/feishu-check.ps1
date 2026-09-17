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
#   powershell -File scripts/feishu-check.ps1 -ExpectedRedirectURL ''    # 跳过第 4 项的比对
#   powershell -File scripts/feishu-check.ps1 -SkipCredCheck             # 离线时不打飞书 API
#
# 退出码：0 = 五个观察点全过；1 = 未通电（并打印最可能的成因）。

[CmdletBinding()]
param(
    # 默认对齐 .env.example 里 FEISHU_REDIRECT_URL 的宿主端口（容器映射 18180→8080）。
    [string]$BaseUrl = "http://127.0.0.1:18180",
    # 与 .env 的 FEISHU_REDIRECT_URL 比对。必须是「浏览器会用的那个主机名」，
    # 不是 BaseUrl 的等价写法 —— state 走 Cookie 按主机名隔离，localhost 与
    # 127.0.0.1 在这里不等价。传空串跳过本项断言。
    [string]$ExpectedRedirectURL = "http://localhost:18180/auth/callback",
    [int]$Timeout = 10,
    # 第 1 项要访问 open.feishu.cn。离线 / 代理受限时用它跳过，只验应用侧四项。
    [switch]$SkipCredCheck
)

$ErrorActionPreference = "Stop"

function Write-Step([string]$m) { Write-Host "[feishu] $m" }
function Write-Pass([string]$m) { Write-Host "[feishu] OK   $m" }
function Write-Fail([string]$m) { Write-Host "[feishu] FAIL $m" }

# 从 curl -D - 的原始头里取状态码。PowerShell 5.1 没有 -SkipHttpErrorCheck，
# Invoke-WebRequest 遇到 303/500 会抛异常，拿不到头 —— 与 ratelimit-check.ps1 同理。
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

# ---------- 1/5 凭据本身是否有效 ----------
# 判据：拿 .env 里的 App ID + Secret 去打 tenant_access_token/internal。
# 该端点会区分两种失败（实测对照）：
#   假 app_id + 真 secret → 10014 "app id not exists"
#   真 app_id + 假 secret → 10014 "app secret invalid"
# 所以 code=10014 时看 msg 就能定位到具体是哪一件凭据错。
if (-not $SkipCredCheck) {
    $repoRoot = Split-Path -Parent $PSScriptRoot
    $envFile = Join-Path $repoRoot '.env'
    $credProblems = @()

    if (-not (Test-Path $envFile)) {
        $credProblems += "读不到 $envFile —— 凭据写在仓库根的 .env（已被 .gitignore 忽略，不会进仓库）"
    } else {
        $envMap = @{}
        Get-Content -LiteralPath $envFile -Encoding UTF8 | ForEach-Object {
            if ($_ -match '^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$') {
                $envMap[$Matches[1]] = $Matches[2].Trim()
            }
        }
        foreach ($k in 'FEISHU_APP_ID', 'FEISHU_APP_SECRET') {
            if ([string]::IsNullOrWhiteSpace($envMap[$k])) { $credProblems += ".env 里 $k 为空或缺失" }
        }

        if ($credProblems.Count -eq 0) {
            # PowerShell 5.1 把内联 JSON 交给原生 exe 会破坏引号（→ 服务端收到非法 JSON 返 400），
            # 必须落成临时文件再用 --data-binary "@文件"。secret 不落 stdout。
            $tmpJson = Join-Path $env:TEMP ("feishu-cred-{0}.json" -f ([guid]::NewGuid().ToString('N')))
            $payload = @{
                app_id     = $envMap['FEISHU_APP_ID']
                app_secret = $envMap['FEISHU_APP_SECRET']
            } | ConvertTo-Json -Compress
            [System.IO.File]::WriteAllText($tmpJson, $payload, (New-Object System.Text.UTF8Encoding $false))
            try {
                $raw = & curl.exe -s --max-time $Timeout -X POST `
                    'https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal' `
                    -H 'Content-Type: application/json; charset=utf-8' `
                    --data-binary "@$tmpJson" 2>$null
            } finally {
                Remove-Item -LiteralPath $tmpJson -Force -ErrorAction SilentlyContinue
            }

            $cred = $null
            try { $cred = $raw | ConvertFrom-Json } catch { }
            if ($null -eq $cred) {
                $credProblems += "凭据校验无有效响应（网络不通？加 -SkipCredCheck 可跳过本项）：$raw"
            } elseif ([int]$cred.code -eq 0) {
                Write-Pass "1/5 凭据有效（tenant_access_token 换发成功）"
            } elseif ("$($cred.msg)" -match 'secret') {
                $credProblems += "App Secret 无效（飞书原话：$($cred.msg)）"
                Write-Host "      常见成因：① 从截图/聊天里抄错一个字符；② 在后台点过 Secret 右侧的"
                Write-Host "        「重置」循环箭头，旧值立刻作废，而 .env 没跟着更新。"
                Write-Host "      处置：回「凭证与基础信息」重新复制 App Secret → 更新 .env → 重建容器。"
            } elseif ("$($cred.msg)" -match 'app id') {
                $credProblems += "App ID 无效（飞书原话：$($cred.msg)）"
                Write-Host "      若应用建在 Lark 国际版（open.larksuite.com），本项目端点常量写死为飞书"
                Write-Host "      国内版（internal/auth/feishu.go 的 feishuTokenURL / feishuUserInfoURL），"
                Write-Host "      两边域名不同，需先决定用哪一版。"
            } else {
                $credProblems += "凭据校验失败：code=$($cred.code) msg=$($cred.msg)"
            }
        }
    }

    if ($credProblems.Count -gt 0) {
        Write-Fail "1/5 凭据不可用："
        $credProblems | ForEach-Object { Write-Host "       - $_" }
        Write-Host ""
        Write-Host "注意：这一项不过时，后面四项可能**全过** —— 真人授权到换 token 那一步才会"
        Write-Host "      炸出 invalid_client，且那时授权码已被消费，必须重新点一次授权。先修这里。"
        exit 1
    }
} else {
    Write-Step "1/5 已按 -SkipCredCheck 跳过凭据校验（只验应用侧四项）"
}

# ---------- 2/5 服务可达 + login 状态 ----------
$healthRaw = & curl.exe -s --max-time $Timeout "$BaseUrl/health" 2>$null
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($healthRaw)) {
    Write-Fail "2/5 GET /health 无响应。先确认服务在跑：docker ps，或 make run"
    exit 1
}

$health = $null
try { $health = $healthRaw | ConvertFrom-Json } catch {
    Write-Fail "2/5 /health 返回的不是 JSON：$healthRaw"
    exit 1
}

$feishuOn = [bool]$health.data.login.feishu
$localOn = [bool]$health.data.login.local
Write-Step "2/5 /health   login.feishu=$feishuOn   login.local=$localOn"

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

# ---------- 3/5 飞书入口路由 ----------
$loginHeaders = & curl.exe -s -D - -o NUL --max-time $Timeout "$BaseUrl/auth/login?provider=feishu" 2>$null
$loginStatus = Get-StatusCode $loginHeaders

if ($loginStatus -ne 302) {
    Write-Fail "3/5 GET /auth/login?provider=feishu 返回 $loginStatus，期望 302"
    $body = & curl.exe -s --max-time $Timeout "$BaseUrl/auth/login?provider=feishu" 2>$null
    if (-not [string]::IsNullOrWhiteSpace($body)) { Write-Host "      响应体：$body" }
    Write-Host "      400 且含 'feishu login is not configured' = 配置读取层的问题（回到第 1 步）"
    exit 1
}
Write-Pass "3/5 /auth/login?provider=feishu → 302"

$locLine = $loginHeaders | Where-Object { $_ -match '^(?i)location:' } | Select-Object -First 1
if (-not $locLine) {
    Write-Fail "3/5 返回 302 但没有 Location 头"
    exit 1
}
$authorizeURL = ($locLine -replace '^(?i)location:\s*', '').Trim()
Write-Step "      授权页：$authorizeURL"

$stateCookie = $loginHeaders | Where-Object { $_ -match 'eino_oauth_state' } | Select-Object -First 1
if (-not $stateCookie) {
    Write-Fail "3/5 没有种下 state Cookie（eino_oauth_state），回调时 state 校验必然失败"
    exit 1
}
Write-Pass "      已种下 state Cookie（eino_oauth_state，Path=/auth，5 分钟）"

# ---------- 4/5 授权 URL 参数自洽 ----------
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
    Write-Fail "4/5 授权 URL 参数有问题："
    $problems | ForEach-Object { Write-Host "       - $_" }
    Write-Host "      如果只是主机名写法不同（localhost vs 127.0.0.1），用 -ExpectedRedirectURL '' 跳过本项"
    exit 1
}
Write-Pass "4/5 授权 URL 参数自洽（client_id / scope / response_type / state / redirect_uri）"

# ---------- 5/5 回调路由与 state 校验 ----------
$cbStatus = & curl.exe -s -o NUL -w "%{http_code}" --max-time $Timeout "$BaseUrl/auth/callback?state=bogus&code=bogus" 2>$null
if ("$cbStatus" -ne "400") {
    Write-Fail "5/5 /auth/callback 对伪造 state 返回 $cbStatus，期望 400 —— state 校验可能没生效"
    exit 1
}
Write-Pass "5/5 /auth/callback 拒绝伪造 state（400），回调路由活着"

Write-Host ""
Write-Pass "五个观察点全过 —— 链路已通电，剩下的只能靠浏览器"
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
