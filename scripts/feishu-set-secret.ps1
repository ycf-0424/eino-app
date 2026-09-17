# feishu-set-secret.ps1 —— 更新 .env 里的 FEISHU_APP_SECRET，**先验证再落盘**。
#
# 为什么需要它：App Secret 是本项目唯一「填错了也不报错、一路绿灯到真人授权才炸」的配置项。
#   /health 的 login.feishu 只反映三个值非空（值错也照样 true）；
#   授权页能正常打开、能授权、能回跳；直到服务端换 token 那一步才回
#     {"error":"invalid_client","error_description":"The client secret is invalid.","code":20002}
#   而那时授权码已作废 —— 实测拿到后 3 分半即报 20004 "code has expired"，
#   且回调一被处理就立刻清掉 state Cookie，刷新那页只会得到 400 state mismatch
#   —— 只能回 /auth/login 重新发起，一次浏览器往返很贵（2026-09-17 实测踩过）。
# 本脚本把「验证」放到「写入」之前：飞书不认这个值，就一个字节都不动 .env。
#
# 用法：
#   powershell -File scripts/feishu-set-secret.ps1                  # 交互式粘贴（不回显）
#   powershell -File scripts/feishu-set-secret.ps1 -DryRun          # 只验证，不写 .env
#   powershell -File scripts/feishu-set-secret.ps1 -Secret xxx      # 脚本化调用
#   powershell -File scripts/feishu-set-secret.ps1 -AppID cli_xxx   # 覆盖 App ID（默认读 .env）
#
# 退出码：0 = 凭据有效（已写入 / DryRun 未写）；1 = 未写入（原因见输出）。

[CmdletBinding()]
param(
    # 不传则交互式提示（Read-Host -AsSecureString，粘贴不回显）。
    [string]$Secret,
    # 不传则从 .env 的 FEISHU_APP_ID 读。
    [string]$AppID,
    # 留空则用仓库根的 .env。注意不能在 param 默认值里算 —— PS 5.1 求值 param()
    # 默认值时 $PSScriptRoot 还是空的（只在脚本体里才有值），会在绑定阶段就报错。
    [string]$EnvFile = '',
    [int]$Timeout = 15,
    # 只验证凭据，不动 .env。
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($EnvFile)) {
    $EnvFile = Join-Path (Split-Path -Parent $PSScriptRoot) '.env'
}

function Write-Step([string]$m) { Write-Host "[secret] $m" }
function Write-Pass([string]$m) { Write-Host "[secret] OK   $m" }
function Write-Fail([string]$m) { Write-Host "[secret] FAIL $m" }

# ---------- 1. 取 Secret（交互式时不回显） ----------
if ([string]::IsNullOrWhiteSpace($Secret)) {
    $secure = Read-Host -Prompt "[secret] 请粘贴 App Secret（回车确认，不回显）" -AsSecureString
    $bstr = [System.Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    try { $Secret = [System.Runtime.InteropServices.Marshal]::PtrToStringAuto($bstr) }
    finally { [System.Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr) }
}

# 从网页复制常带尾部空白/换行；不去掉会被飞书判成 invalid_client（且肉眼看不出来）。
$rawSecret = $Secret
$Secret = $Secret.Trim()
if ($Secret -ne $rawSecret) { Write-Step "已去掉首尾空白（$($rawSecret.Length) → $($Secret.Length) 字符）" }
if ($Secret.Length -eq 0) { Write-Fail "Secret 为空，未做任何修改"; exit 1 }
if ($Secret -notmatch '^[A-Za-z0-9]+$') {
    Write-Step "注意：Secret 含非字母数字字符，飞书 App Secret 通常只有字母和数字 —— 确认没混进中文空格或全角字符"
}

# ---------- 2. 取 .env 原文 + App ID + 旧 Secret ----------
$envText = ''
$oldSecret = ''
if (Test-Path $EnvFile) {
    $envText = [System.IO.File]::ReadAllText($EnvFile)
    # 用 [ \t] 而不是 \s：多行模式下 \s 会跨越换行，可能把键名前的空行一起吃掉。
    if ($envText -match '(?m)^[ \t]*FEISHU_APP_SECRET[ \t]*=[ \t]*([^\r\n]*)$') { $oldSecret = $Matches[1].Trim() }
} else {
    Write-Step "目标 .env 不存在，将新建：$EnvFile"
}

if ([string]::IsNullOrWhiteSpace($AppID)) {
    if ($envText -match '(?m)^[ \t]*FEISHU_APP_ID[ \t]*=[ \t]*([^\r\n]+)$') {
        $AppID = $Matches[1].Trim()
    } else {
        Write-Fail "$EnvFile 里没有 FEISHU_APP_ID，且未用 -AppID 传入。App ID 就在同一个页面上，先补上。"
        exit 1
    }
}
Write-Step "App ID：$AppID"

# ---------- 3. 先验证：拿这对凭据换 tenant_access_token ----------
# 该端点会区分失败维度（实测对照）：
#   假 app_id + 真 secret → 10014 "app id not exists"
#   真 app_id + 假 secret → 10014 "app secret invalid"
# PowerShell 5.1 把内联 JSON 交给原生 exe 会破坏引号 → 必须落文件再 --data-binary "@文件"。
$tmpJson = Join-Path $env:TEMP ("feishu-secret-{0}.json" -f ([guid]::NewGuid().ToString('N')))
$payload = @{ app_id = $AppID; app_secret = $Secret } | ConvertTo-Json -Compress
[System.IO.File]::WriteAllText($tmpJson, $payload, (New-Object System.Text.UTF8Encoding $false))
try {
    $raw = & curl.exe -s --max-time $Timeout -X POST `
        'https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal' `
        -H 'Content-Type: application/json; charset=utf-8' `
        --data-binary "@$tmpJson" 2>$null
} finally {
    Remove-Item -LiteralPath $tmpJson -Force -ErrorAction SilentlyContinue
}

$resp = $null
try { $resp = $raw | ConvertFrom-Json } catch { }
if ($null -eq $resp) {
    Write-Fail "凭据校验无有效响应（网络/代理？）：$raw"
    Write-Fail "未修改 $EnvFile"
    exit 1
}

if ([int]$resp.code -ne 0) {
    Write-Fail "飞书拒绝这对凭据：code=$($resp.code) msg=$($resp.msg)"
    if ("$($resp.msg)" -match 'secret') {
        Write-Host "       这个 App Secret 对 App ID $AppID 无效。两种可能："
        Write-Host "         a) 粘贴时抄错 / 少了字符（对比长度：飞书 App Secret 通常 32 位）"
        Write-Host "         b) 它已被「重置」（Secret 右侧的循环箭头）—— 旧值会立刻作废"
        Write-Host "       处置：回「凭证与基础信息」，点 App Secret 右侧的**复制图标**"
        Write-Host "             （不要用眼睛图标手抄），然后重新跑本脚本。"
    } elseif ("$($resp.msg)" -match 'app id') {
        Write-Host "       这个 App ID 在飞书不存在。若应用建在 Lark 国际版（open.larksuite.com），"
        Write-Host "       本项目端点常量写死为飞书国内版（internal/auth/feishu.go），两边域名不同。"
    }
    Write-Fail "未修改 $EnvFile（当前内容一个字节都没动）"
    exit 1
}

Write-Pass "凭据有效：tenant_access_token 换发成功（App ID 与 Secret 配成一对）"

if ($oldSecret -eq $Secret) {
    Write-Step "提示：新值与 .env 里原来的**完全一致** —— 说明不是抄错，而是这个 Secret 之前就失效了。"
}

# ---------- 4. 验证通过才写盘 ----------
if ($DryRun) {
    Write-Step "-DryRun：已验证通过，未写入 $EnvFile"
    exit 0
}

$newLine = "FEISHU_APP_SECRET=$Secret"
if ($envText -match '(?m)^[ \t]*FEISHU_APP_SECRET[ \t]*=') {
    # 只替换这一行，其余字节（含 MySQL 口令、CRLF 行尾）原样保留。
    # 模式停在 [^\r\n]*、不带 $ —— 行尾的 \r 不被吞掉，CRLF 文件不会被改出混合行尾。
    $envText = [regex]::Replace($envText, '(?m)^[ \t]*FEISHU_APP_SECRET[ \t]*=[^\r\n]*', { param($m) $newLine })
} else {
    if ($envText.Length -gt 0 -and -not $envText.EndsWith("`n")) { $envText += "`n" }
    $envText += $newLine + "`n"
    Write-Step "原文件没有 FEISHU_APP_SECRET 行，已追加"
}
# UTF-8 无 BOM：dotenv 与 docker compose 都能读，BOM 反而会污染第一个键名。
[System.IO.File]::WriteAllText($EnvFile, $envText, (New-Object System.Text.UTF8Encoding $false))
Write-Pass "已写入 $EnvFile（保持 UTF-8 无 BOM，其余行未改动）"

Write-Host ""
Write-Host "接下来两步（缺一不可）："
Write-Host "  1) 重建容器 —— environment 只在容器创建时注入，up -d 不生效："
Write-Host "       docker compose -f docker-compose.milvus.yml up -d --force-recreate app"
Write-Host "  2) 复检五个观察点："
Write-Host "       powershell -File scripts/feishu-check.ps1"
Write-Host ""
Write-Host "然后浏览器重新走一次授权（旧授权码已作废，必须重新点）："
Write-Host "  http://localhost:18180/auth/login   ← 必须用 localhost，与 FEISHU_REDIRECT_URL 同主机名"
exit 0
