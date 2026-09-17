<#
.SYNOPSIS
  一对「App ID + App Secret」被飞书拒绝时，定位是不是手抄误读了「形近字」。

.DESCRIPTION
  真实案例（2026-09-17）：控制台的 App Secret 末两位是 ...R53lY（小写 L），
  人工从截图抄成 ...R53IY（大写 i）。l 与 I 在常见字体里几乎同形，
  而飞书的报错只说 "app secret invalid"，不告诉你是第几位错。
  后果是链路一路绿灯到「换 token」才炸，白白多跑好几趟浏览器授权。

  本脚本对 App Secret 生成「形近字变体」——
  0/O/o、i/1/l/I、5/S、8/B、z/2、6/G、9/g，以及逐位大小写互换 ——
  逐个打到飞书 tenant_access_token 端点，由飞书当裁判。命中即打印正确值。

  适用前提：凭据是你自己从截图/纸面上手抄的，且已确认应用本身存在
  （App ID 打过去返回的是 "app secret invalid" 而不是 "app id not exists"）。
  **不要**用它去试探不属于你的凭据。

  只覆盖「单字符误读」。连续错两个字符的情况不在这里的搜索空间内 ——
  正解是别手抄：控制台点复制图标，再跑
  `scripts/feishu-set-secret.ps1 -FromClipboard`（先验证再落盘）。

.PARAMETER Secret
  待检查的 App Secret。不传则依次尝试：剪贴板 → .env 里的现值。

.PARAMETER AppID
  应用标识。不传则从 .env 的 FEISHU_APP_ID 读。

.PARAMETER Write
  命中后把正确值写回 .env（仍然只改 FEISHU_APP_SECRET 一行）。不加则只报告。

.EXAMPLE
  powershell -File scripts/feishu-probe-secret.ps1
  powershell -File scripts/feishu-probe-secret.ps1 -Write
#>
param(
    [string]$Secret,
    [string]$AppID,
    [switch]$Write,
    [string]$EnvFile,
    [int]$Timeout = 15
)

$ErrorActionPreference = "Stop"

# PS 5.1 的坑：param() 默认值里 $PSScriptRoot 为空，必须在函数体里解析。
if (-not $EnvFile) { $EnvFile = Join-Path (Split-Path -Parent $PSScriptRoot) '.env' }
if (-not (Test-Path -LiteralPath $EnvFile)) { Write-Host "[probe] FAIL 找不到 $EnvFile"; exit 1 }

function Write-Step($m) { Write-Host "[probe] $m" }
function Write-Pass($m) { Write-Host "[probe] OK   $m" }
function Write-Fail($m) { Write-Host "[probe] FAIL $m" }

$envText = [System.IO.File]::ReadAllText($EnvFile)

if (-not $AppID) {
    if ($envText -match '(?m)^[ \t]*FEISHU_APP_ID[ \t]*=[ \t]*([^\r\n]+)') {
        $AppID = $Matches[1].Trim()
    }
}
if (-not $AppID) { Write-Fail "没有 App ID：请用 -AppID 指定，或在 .env 里配好 FEISHU_APP_ID"; exit 1 }

$currentSecret = ''
if ($envText -match '(?m)^[ \t]*FEISHU_APP_SECRET[ \t]*=[ \t]*([^\r\n]+)') {
    $currentSecret = $Matches[1].Trim()
}

if (-not $Secret) {
    try {
        $clip = Get-Clipboard -Raw
        if ($null -ne $clip) { $Secret = $clip.Trim() }
    } catch { }
    if (-not $Secret) { $Secret = $currentSecret }
}
if (-not $Secret) { Write-Fail "没有可检查的 Secret：请用 -Secret 指定，或先复制到剪贴板"; exit 1 }

$Secret = $Secret.Trim()
Write-Step "App ID : $AppID"
Write-Step "Secret : $($Secret.Substring(0,4))…$($Secret.Substring($Secret.Length-4))（长度 $($Secret.Length)）"

# ---------- 生成形近字变体 ----------
# 注意：PowerShell 的哈希表字面量默认**大小写不敏感**，'o' 与 'O'、'i' 与 'I'、'g' 与 'G'
# 会撞键（实测报 "Duplicate keys ... are not allowed in hash literals"）。
# 这里用 switch -CaseSensitive，天然区分大小写。
function Get-GlyphAlternatives([char]$ch) {
    $out = @()
    switch -CaseSensitive ([string]$ch) {
        '0' { $out = @('O') }
        'O' { $out = @('0') }
        'o' { $out = @('0', 'O') }
        'i' { $out = @('1', 'l', 'I') }
        'I' { $out = @('l', '1', 'i') }
        'l' { $out = @('1', 'i', 'I') }
        '1' { $out = @('i', 'l', 'I') }
        '5' { $out = @('S') }
        'S' { $out = @('5') }
        '8' { $out = @('B') }
        'B' { $out = @('8') }
        'z' { $out = @('2') }
        '2' { $out = @('z') }
        '6' { $out = @('G') }
        'G' { $out = @('6') }
        '9' { $out = @('g') }
        'g' { $out = @('9') }
    }
    return $out
}

$variants = New-Object System.Collections.ArrayList
$seen = New-Object 'System.Collections.Generic.HashSet[string]'
[void]$seen.Add($Secret)
[void]$variants.Add([pscustomobject]@{ Value = $Secret; Tag = '(原样)' })

for ($i = 0; $i -lt $Secret.Length; $i++) {
    $ch = $Secret[$i]
    $pos = $i + 1
    $cands = New-Object System.Collections.ArrayList
    $key = $ch.ToString()
    foreach ($a in (Get-GlyphAlternatives $ch)) { [void]$cands.Add($a) }
    if ([char]::IsLetter($ch)) {
        $swapped = if ([char]::IsUpper($ch)) { $ch.ToString().ToLowerInvariant() } else { $ch.ToString().ToUpperInvariant() }
        [void]$cands.Add($swapped)
    }
    foreach ($alt in $cands) {
        $v = $Secret.Substring(0, $i) + $alt + $Secret.Substring($i + 1)
        if ($seen.Add($v)) {
            [void]$variants.Add([pscustomobject]@{ Value = $v; Tag = "第 $pos 位 $key -> $alt" })
        }
    }
}

Write-Step "生成 $($variants.Count) 个候选（含原样），逐个请飞书裁决…"
Write-Host ""

$uri = 'https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal'
$hit = $null
$n = 0
foreach ($item in $variants) {
    $n++
    $tmpJson = Join-Path $env:TEMP ("feishu-probe-{0}.json" -f ([guid]::NewGuid().ToString('N')))
    $payload = @{ app_id = $AppID; app_secret = $item.Value } | ConvertTo-Json -Compress
    [System.IO.File]::WriteAllText($tmpJson, $payload, (New-Object System.Text.UTF8Encoding $false))
    try {
        $raw = & curl.exe -s --max-time $Timeout -X POST $uri `
            -H 'Content-Type: application/json; charset=utf-8' `
            --data-binary "@$tmpJson" 2>$null
    } finally {
        Remove-Item -LiteralPath $tmpJson -Force -ErrorAction SilentlyContinue
    }

    $resp = $null
    try { $resp = $raw | ConvertFrom-Json } catch { }
    if ($null -eq $resp) {
        Write-Step "#$n $($item.Tag)：无有效响应（网络/代理？）"
        continue
    }

    if ([int]$resp.code -eq 0) {
        Write-Pass "命中！#$n $($item.Tag)"
        $hit = $item
        break
    } elseif ("$($resp.msg)" -match 'app id not exists') {
        # 这个 App ID 本身不存在，继续试 secret 没有意义。
        Write-Fail "飞书说这个 App ID 不存在（code=$($resp.code)）。先确认 App ID，再谈 Secret。"
        exit 1
    } else {
        if ($item.Tag -eq '(原样)') { Write-Step "#$n 原样 → $($resp.msg)" }
    }
    Start-Sleep -Milliseconds 100
}

Write-Host ""
if (-not $hit) {
    Write-Fail "已试 $($variants.Count) 个变体，全部被拒。"
    Write-Host "       单字符误读之外的可能：错了两处以上字符，或这个值已失效（控制台点过重置）。"
    Write-Host "       正解：控制台按 F5 刷新 → 点 App Secret 右侧**复制图标** → 跑"
    Write-Host "             powershell -File scripts/feishu-set-secret.ps1 -FromClipboard"
    exit 1
}

Write-Pass "正确值：$($hit.Value)"
if ($hit.Tag -ne '(原样)') {
    Write-Host "       与原值的差异：$($hit.Tag)  —— 手抄误读形近字，凭据本身没问题。"
}
Write-Host "       教训：凭据不要从截图手抄，用复制图标（本项目的 -FromClipboard 就是为此）。"

if (-not $Write) {
    Write-Step "未写 .env（加 -Write 才会写）。"
    exit 0
}

if ($currentSecret -eq $hit.Value) {
    Write-Step ".env 里已经是这个值，无需改动。"
    exit 0
}

$newLine = "FEISHU_APP_SECRET=$($hit.Value)"
if ($envText -match '(?m)^[ \t]*FEISHU_APP_SECRET[ \t]*=') {
    # 只替换这一行：模式停在 [^\r\n]*、不带 $，CRLF 的 \r 不被吞掉。
    $envText = [regex]::Replace($envText, '(?m)^[ \t]*FEISHU_APP_SECRET[ \t]*=[^\r\n]*', { param($m) $newLine })
} else {
    if ($envText.Length -gt 0 -and -not $envText.EndsWith("`n")) { $envText += "`n" }
    $envText += $newLine + "`n"
}
[System.IO.File]::WriteAllText($EnvFile, $envText, (New-Object System.Text.UTF8Encoding $false))
Write-Pass "已写回 $EnvFile（其余行未改动）"
Write-Host ""
Write-Host "接下来："
Write-Host "  1) docker compose -f docker-compose.milvus.yml up -d --force-recreate app"
Write-Host "  2) powershell -File scripts/feishu-check.ps1"
Write-Host "  3) 浏览器 http://localhost:18180/auth/login 重新授权"
exit 0
