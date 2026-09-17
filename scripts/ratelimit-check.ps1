# ratelimit-check.ps1 —— 验证 POST /chat 的进程内限流（步骤 5.2 验收）。
#
# 为什么需要它：步骤 5.2 的验收写的是「脚本连续打 40 次 POST /chat 触发 429」。
# 这条只能实测 —— 单测验证的是令牌桶算法本身，而这里验证的是「中间件真的挂在
# 路由上、key 真的按 owner/IP 构造、429 真的带 Retry-After」。
#
# 为什么用 curl.exe 而不是 Invoke-WebRequest：Windows PowerShell 5.1 没有
# -SkipHttpErrorCheck，429 会直接抛异常，拿不到状态码与响应头；curl 用 -w 与 -D
# 输出状态码和响应头，5.1 与 7 下行为一致。
#
# 用法：
#   pwsh -File scripts/ratelimit-check.ps1                                # 打本机 18181
#   pwsh -File scripts/ratelimit-check.ps1 -BaseUrl http://localhost:18180
#   pwsh -File scripts/ratelimit-check.ps1 -Cookie "eino_session=xxx"     # 认证打开时按 owner 计数
#
# 退出码：0 = 观察到的行为与配置一致；1 = 不一致（429 没出现 / 缺少 Retry-After）。

[CmdletBinding()]
param(
    [string]$BaseUrl = "http://127.0.0.1:18181",
    [int]$Requests = 40,
    # 认证打开时用 Cookie 打，让限流按 owner 而不是按 IP 计数。
    [string]$Cookie = "",
    [string]$SessionId = "",
    # 单次请求超时（秒）。放行的那几次会真的打到模型上，本地模型首次加载可能较慢。
    [int]$Timeout = 120
)

$ErrorActionPreference = "Stop"

function Write-Step([string]$Message) { Write-Host "[ratelimit] $Message" }

if ($Requests -lt 2) { Write-Step "ERROR: -Requests 至少为 2"; exit 1 }

# 字段名必须是 query：POST /chat 的请求体是 {session_id, query, skill}。
# 曾经这里写的是 message，结果放行的那几次全部被 400 拦下 —— 限流结论仍然成立
# （429 在到达 handler 之前就产生），但脚本就没在测真实的聊天路径了。
$body = '{"query":"限流探测：只回一个字符"}'
if (-not [string]::IsNullOrWhiteSpace($SessionId)) {
    $body = '{"query":"限流探测：只回一个字符","session_id":"' + $SessionId + '"}'
}

$workDir = Join-Path $env:TEMP ("ratelimit-check-" + [Guid]::NewGuid().ToString("N").Substring(0, 8))
New-Item -ItemType Directory -Path $workDir -Force | Out-Null
$bodyFile = Join-Path $workDir "body.txt"
$headFile = Join-Path $workDir "head.txt"
$payloadFile = Join-Path $workDir "payload.json"
[System.IO.File]::WriteAllText($payloadFile, $body, (New-Object System.Text.UTF8Encoding($false)))

Write-Step "target=$BaseUrl requests=$Requests cookie=$(if ($Cookie) { 'yes' } else { 'no' })"

$statuses = @()
$retryAfters = @()
$sampleBody = ""
$sampleRetry = ""
try {
    for ($i = 1; $i -le $Requests; $i++) {
        $curlArgs = @("-s", "-m", "$Timeout", "-o", $bodyFile, "-D", $headFile, "-w", "%{http_code}",
            "-X", "POST", "-H", "Content-Type: application/json")
        if (-not [string]::IsNullOrWhiteSpace($Cookie)) { $curlArgs += @("-H", "Cookie: $Cookie") }
        $curlArgs += @("--data-binary", "@$payloadFile", "$BaseUrl/chat")

        $code = (& curl.exe @curlArgs 2>$null | Out-String).Trim()
        if ($code -notmatch '^\d{3}$') { $code = "0" }
        $statuses += [int]$code

        $retry = ""
        if ($code -eq "429") {
            $match = Select-String -LiteralPath $headFile -Pattern '^Retry-After:\s*(.+)$' -ErrorAction SilentlyContinue
            if ($match) { $retry = $match.Matches[0].Groups[1].Value.Trim() }
            if ([string]::IsNullOrWhiteSpace($sampleRetry) -and -not [string]::IsNullOrWhiteSpace($retry)) {
                $sampleRetry = $retry
                $sampleBody = (Get-Content -LiteralPath $bodyFile -Raw -Encoding UTF8).Trim()
            }
        }
        $retryAfters += $retry
    }
} finally {
    Remove-Item -LiteralPath $workDir -Recurse -Force -ErrorAction SilentlyContinue
}

$throttledIndexes = @()
for ($i = 0; $i -lt $statuses.Count; $i++) {
    if ($statuses[$i] -eq 429) { $throttledIndexes += ($i + 1) }
}
$throttledCount = $throttledIndexes.Count
$firstThrottle = if ($throttledCount -gt 0) { $throttledIndexes[0] } else { -1 }
$missingRetryAfter = 0
for ($i = 0; $i -lt $statuses.Count; $i++) {
    if ($statuses[$i] -eq 429 -and [string]::IsNullOrWhiteSpace($retryAfters[$i])) { $missingRetryAfter++ }
}
$okCount = @($statuses | Where-Object { $_ -ge 200 -and $_ -lt 300 }).Count
$otherCount = @($statuses | Where-Object { $_ -ge 400 -and $_ -ne 429 }).Count

Write-Step ("2xx=$okCount other4xx5xx=$otherCount throttled=$throttledCount first_throttle=$firstThrottle retry_after_missing=$missingRetryAfter")
Write-Step ("status_by_index: " + ((1..$statuses.Count | ForEach-Object { "${_}:" + $statuses[$_ - 1] }) -join " "))
if ($throttledCount -gt 0) {
    Write-Step ("sample 429 body: " + $sampleBody)
    Write-Step ("sample Retry-After: " + $sampleRetry)
}

$failed = $false
if ($throttledCount -eq 0) {
    Write-Step "FAIL: $Requests 次请求全部未被限制 —— 限流中间件没生效，或 per_minute 远大于请求数"
    $failed = $true
}
if ($missingRetryAfter -gt 0) {
    Write-Step "FAIL: 有 $missingRetryAfter 个 429 响应缺少 Retry-After 头"
    $failed = $true
}
# 令牌桶语义：桶容量 = burst，新 key 从满桶开始，所以第一次被拒的下标应约等于 burst+1
# （两次请求之间的补充量取决于耗时）。这里只卡「不能太早」——第 1 次就被拒说明桶不是从满的开始的。
if ($firstThrottle -eq 1) {
    Write-Step "FAIL: 第 1 次请求即被拒 —— 新 key 未从满桶开始"
    $failed = $true
}

if ($failed) { exit 1 }
Write-Step "OK: 限流行为与预期一致"
exit 0
