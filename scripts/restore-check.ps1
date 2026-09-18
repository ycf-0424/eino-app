# restore-check.ps1 —— 备份恢复演练（步骤 5.3）。
#
# 「只备份不恢复等于没备份」：真正会失效的不是脚本能跑通，而是某天需要它时
# 才发现 dump 是空的、卷包是坏的、或者表结构缺了半张。这个脚本把那次发现
# 提前到每次演练，代价是一个临时库的空间。
#
# 用法：
#   pwsh -File scripts/restore-check.ps1                  # 演练最近一份备份
#   pwsh -File scripts/restore-check.ps1 -BackupDir data/backups/20260917-120000
#   pwsh -File scripts/restore-check.ps1 -KeepTempDatabase  # 不删临时库，便于人工翻查
#
# 退出码：0 全部一致；1 有任何一项不一致或恢复失败。
# 临时库名固定为 eino_restore_check：演练不需要并行，固定名字便于事后定位。

[CmdletBinding()]
param(
    [string]$BackupDir = "",
    [string]$BackupRoot = "",
    [string]$TempDatabase = "eino_restore_check",
    [switch]$KeepTempDatabase,
    [switch]$SkipChecksum
)

$ErrorActionPreference = "Stop"

$RepoRoot = Split-Path -Parent $PSScriptRoot
$ComposeFile = Join-Path $RepoRoot "docker-compose.milvus.yml"
if ([string]::IsNullOrWhiteSpace($BackupRoot)) {
    $BackupRoot = Join-Path $RepoRoot "data/backups"
}

function Write-Step([string]$Message) { Write-Host "[restore-check] $Message" }
function Fail([string]$Message) { Write-Host "[restore-check] ERROR: $Message" -ForegroundColor Red; exit 1 }

# 与 backup.ps1 保持一致，不依赖可能被定时任务环境覆盖的 Get-FileHash。
function Get-Sha256([string]$Path) {
    $stream = [System.IO.File]::OpenRead($Path)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try {
        return ([BitConverter]::ToString($sha.ComputeHash($stream))).Replace("-", "").ToLowerInvariant()
    } finally {
        $sha.Dispose()
        $stream.Dispose()
    }
}

function Read-DotEnv([string]$Path) {
    $values = @{}
    if (-not (Test-Path $Path)) { return $values }
    foreach ($line in Get-Content -LiteralPath $Path -Encoding UTF8) {
        $trimmed = $line.Trim()
        if ($trimmed -eq "" -or $trimmed.StartsWith("#")) { continue }
        $index = $trimmed.IndexOf("=")
        if ($index -lt 1) { continue }
        $values[$trimmed.Substring(0, $index).Trim()] = $trimmed.Substring($index + 1).Trim().Trim('"').Trim("'")
    }
    return $values
}

function Invoke-Docker([string[]]$Arguments) {
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $output = & docker @Arguments 2>&1
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) {
        throw "docker failed (exit $code): docker $($Arguments -join ' ')`n$output"
    }
    return $output
}

function Invoke-Sql([string]$Statement) {
    $output = Invoke-Docker @("exec", "-e", "MYSQL_PWD=$DbPassword",
        $containerId, "mysql", "-N", "-B", "-u", $DbUser, "-e", $Statement)
    return $output
}

# 解析 manifest 的 [mysql_row_counts] 段。解析失败即视为演练失败：
# 一份读不出行数基线的备份，在真正恢复时给不出任何「恢复是否完整」的判断依据。
function Read-ManifestCounts([string]$Path) {
    $counts = [ordered]@{}
    $inSection = $false
    foreach ($line in Get-Content -LiteralPath $Path -Encoding UTF8) {
        $trimmed = $line.Trim()
        if ($trimmed -eq "[mysql_row_counts]") { $inSection = $true; continue }
        if ($trimmed.StartsWith("[")) { $inSection = $false; continue }
        if (-not $inSection -or $trimmed -eq "") { continue }
        $parts = $trimmed -split " = ", 2
        if ($parts.Count -ne 2) { continue }
        $counts[$parts[0].Trim()] = [int64]$parts[1].Trim()
    }
    return $counts
}

# 解析 [files] 段：<name> sha256=<hash> bytes=<n>
function Read-ManifestFiles([string]$Path) {
    $files = [ordered]@{}
    $inSection = $false
    foreach ($line in Get-Content -LiteralPath $Path -Encoding UTF8) {
        $trimmed = $line.Trim()
        if ($trimmed -eq "[files]") { $inSection = $true; continue }
        if ($trimmed.StartsWith("[")) { $inSection = $false; continue }
        if (-not $inSection -or $trimmed -eq "") { continue }
        $match = [regex]::Match($trimmed, '^(\S+)\s+sha256=([0-9a-f]{64})')
        if ($match.Success) { $files[$match.Groups[1].Value] = $match.Groups[2].Value }
    }
    return $files
}

if ([string]::IsNullOrWhiteSpace($BackupDir)) {
    if (-not (Test-Path $BackupRoot)) { Fail "no backups found at $BackupRoot" }
    # 只认「有 manifest.txt 的」目录：目录名正常但内容缺失，说明那次备份中途失败过。
    # 直接挑最近一份会让演练报「manifest 不存在」，把问题指向错误的方向。
    $candidates = Get-ChildItem -LiteralPath $BackupRoot -Directory |
        Where-Object { $_.Name -match '^\d{8}-\d{6}$' } |
        Sort-Object Name -Descending
    $skipped = @()
    $newest = $null
    foreach ($candidate in $candidates) {
        if (Test-Path (Join-Path $candidate.FullName "manifest.txt")) { $newest = $candidate; break }
        $skipped += $candidate.Name
    }
    if (-not $newest) { Fail "no complete backup (with manifest.txt) under $BackupRoot" }
    if ($skipped.Count -gt 0) {
        Write-Step "WARNING: 跳过 $($skipped.Count) 个不完整备份目录: $($skipped -join ', ')"
    }
    $BackupDir = $newest.FullName
}
$BackupDir = (Resolve-Path -LiteralPath $BackupDir).Path
Write-Step "checking backup: $BackupDir"

$manifestPath = Join-Path $BackupDir "manifest.txt"
if (-not (Test-Path $manifestPath)) { Fail "manifest.txt not found in $BackupDir" }
$dumpPath = Join-Path $BackupDir "mysql.sql"
if (-not (Test-Path $dumpPath)) { Fail "mysql.sql not found in $BackupDir" }
if ((Get-Item -LiteralPath $dumpPath).Length -eq 0) { Fail "mysql.sql is empty" }

# 第一关：校验和。归档损坏必须在连接数据库之前就发现，否则会把「文件坏了」
# 误判成「恢复逻辑坏了」，排查方向从一开始就错。
if (-not $SkipChecksum) {
    $expected = Read-ManifestFiles $manifestPath
    if ($expected.Count -eq 0) { Fail "manifest has no [files] section; cannot verify integrity" }
    foreach ($name in $expected.Keys) {
        $path = Join-Path $BackupDir $name
        if (-not (Test-Path $path)) { Fail "artifact listed in manifest is missing: $name" }
        $actual = Get-Sha256 $path
        if ($actual -ne $expected[$name]) {
            Fail "checksum mismatch for ${name}: manifest=$($expected[$name]) actual=$actual"
        }
        Write-Step "checksum ok: $name"
    }
}

$expectedCounts = Read-ManifestCounts $manifestPath
if ($expectedCounts.Count -eq 0) { Fail "manifest has no [mysql_row_counts] section" }

try {
    Invoke-Docker @("version", "--format", "{{.Server.Version}}") | Out-Null
} catch {
    Fail "Docker engine is not reachable. Start Docker Desktop and retry.`n$_"
}

$settings = Read-DotEnv (Join-Path $RepoRoot ".env")
# 与 backup.ps1 同一套凭据：恢复演练既要读 eino（比对基线不读它，但保持一致），
# 又要在 eino_restore_check 上做 DROP/CREATE/导入。应用账号两样都做不到：
# 它没有临时库的任何权限，而 migrate 账号的授权是按库（ON `eino`.*）给的，
# 对 eino_restore_check 同样无效。
$DbUser = $settings["MYSQL_BACKUP_USER"]
$DbPassword = $settings["MYSQL_BACKUP_PASSWORD"]
if ([string]::IsNullOrWhiteSpace($DbUser) -or [string]::IsNullOrWhiteSpace($DbPassword)) {
    Write-Step "WARNING: MYSQL_BACKUP_* 未配置，回落到应用账号 MYSQL_USER"
    $DbUser = $settings["MYSQL_USER"]
    $DbPassword = $settings["MYSQL_PASSWORD"]
}
if ([string]::IsNullOrWhiteSpace($DbUser) -or [string]::IsNullOrWhiteSpace($DbPassword)) {
    Fail "MYSQL_BACKUP_USER / MYSQL_BACKUP_PASSWORD（或回落的 MYSQL_USER / MYSQL_PASSWORD）必须在 .env 中设置"
}

$containerId = (Invoke-Docker @("compose", "-f", $ComposeFile, "ps", "-q", "mysql")) | Select-Object -First 1
if ([string]::IsNullOrWhiteSpace($containerId)) { Fail "mysql container is not running. Run 'make infra-up' first." }
$containerId = $containerId.Trim()

# 临时库名做白名单校验：它会拼进 SQL，不能来自不可信输入。
if ($TempDatabase -notmatch '^[A-Za-z0-9_]{1,64}$') { Fail "invalid temp database name: $TempDatabase" }

$restoreTarget = "/tmp/eino-restore-check.sql"
$exitCode = 0
try {
    Write-Step "recreating temp database $TempDatabase"
    Invoke-Sql "DROP DATABASE IF EXISTS ``$TempDatabase``; CREATE DATABASE ``$TempDatabase`` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;" | Out-Null

    Write-Step "loading dump into $TempDatabase"
    Invoke-Docker @("cp", $dumpPath, "${containerId}:$restoreTarget") | Out-Null
    # 用 `bash -c` 是为了容器内重定向：宿主机不装 mysql 客户端，而
    # docker exec 的输入重定向在 PowerShell 下同样会被文本编码改写。
    Invoke-Docker @("exec", "-e", "MYSQL_PWD=$DbPassword", $containerId,
        "bash", "-c", "mysql -u `"$DbUser`" -D `"$TempDatabase`" < $restoreTarget") | Out-Null

    Write-Step "comparing row counts"
    $mismatches = @()
    foreach ($table in $expectedCounts.Keys) {
        $expected = $expectedCounts[$table]
        $actualRaw = Invoke-Sql "SELECT COUNT(*) FROM ``$TempDatabase``.``$table``;"
        if ($null -eq $actualRaw -or ($actualRaw | Measure-Object).Count -eq 0) {
            Write-Host ("  MISSING  {0,-40} expected={1}" -f $table, $expected) -ForegroundColor Red
            $mismatches += $table
            continue
        }
        $actual = [int64]($actualRaw | Select-Object -First 1).ToString().Trim()
        if ($actual -eq $expected) {
            Write-Host ("  ok       {0,-40} rows={1}" -f $table, $actual)
        } else {
            Write-Host ("  MISMATCH {0,-40} expected={1} actual={2}" -f $table, $expected, $actual) -ForegroundColor Red
            $mismatches += $table
        }
    }

    if ($mismatches.Count -gt 0) {
        Write-Step "FAILED: $($mismatches.Count) of $($expectedCounts.Count) table(s) differ"
        $exitCode = 1
    } else {
        Write-Step "OK: all $($expectedCounts.Count) table(s) match the manifest"
    }
} catch {
    Write-Step "ERROR during restore: $_"
    $exitCode = 1
} finally {
    # 临时库必须清掉：留着它会占空间，也会让下次演练的 CREATE DATABASE 结果
    # 依赖上一次的状态。清理失败不改结论，但要显式提示。
    if ($KeepTempDatabase) {
        Write-Step "temp database $TempDatabase kept on request"
    } else {
        try {
            Invoke-Sql "DROP DATABASE IF EXISTS ``$TempDatabase``;" | Out-Null
            Write-Step "temp database $TempDatabase dropped"
        } catch {
            Write-Step "WARNING: failed to drop $TempDatabase : $_"
        }
    }
    try {
        Invoke-Docker @("exec", $containerId, "rm", "-f", $restoreTarget) | Out-Null
    } catch {
        # 容器内临时文件残留无实际影响，不因此判定演练失败。
    }
}

exit $exitCode
