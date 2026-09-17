# backup.ps1 —— 全量备份 MySQL 与 Milvus 数据卷（步骤 5.3）。
#
# 为什么不是「复制 MySQL 数据目录」：运行中的 InnoDB 数据目录随时在写，
# 直接 tar 出来的是「某一瞬间的字节」，不是一致性快照，恢复时大概率起不来。
# mysqldump 走事务读，产出的 SQL 是自洽的，所以 MySQL 用 dump，Milvus 用卷打包。
#
# 用法：
#   pwsh -File scripts/backup.ps1                 # 正常备份
#   pwsh -File scripts/backup.ps1 -Keep 3         # 只保留最近 3 份（演练保留策略用）
#   pwsh -File scripts/backup.ps1 -SkipVolumes    # 只备 MySQL（Milvus 卷很大时）
#
# 依赖：Docker 引擎在运行、compose 文件里的 mysql 服务可用。
# 不在宿主机安装 mysql 客户端：容器里就有，且凭据本来就在 .env 里给容器用过。

[CmdletBinding()]
param(
    [string]$BackupRoot = "",
    [int]$Keep = 14,
    [switch]$SkipVolumes
)

$ErrorActionPreference = "Stop"

$RepoRoot = Split-Path -Parent $PSScriptRoot
$ComposeFile = Join-Path $RepoRoot "docker-compose.milvus.yml"
if ([string]::IsNullOrWhiteSpace($BackupRoot)) {
    $BackupRoot = Join-Path $RepoRoot "data/backups"
}

function Write-Step([string]$Message) { Write-Host "[backup] $Message" }
function Fail([string]$Message) { Write-Host "[backup] ERROR: $Message" -ForegroundColor Red; exit 1 }

# .env 是凭据的唯一来源（compose 也读它）。缺变量时不猜默认值：
# 备错库、用错账号都会产出「看起来成功」的坏备份。
function Read-DotEnv([string]$Path) {
    $values = @{}
    if (-not (Test-Path $Path)) { return $values }
    foreach ($line in Get-Content -LiteralPath $Path -Encoding UTF8) {
        $trimmed = $line.Trim()
        if ($trimmed -eq "" -or $trimmed.StartsWith("#")) { continue }
        $index = $trimmed.IndexOf("=")
        if ($index -lt 1) { continue }
        $key = $trimmed.Substring(0, $index).Trim()
        $value = $trimmed.Substring($index + 1).Trim().Trim('"').Trim("'")
        $values[$key] = $value
    }
    return $values
}

function Invoke-Docker([string[]]$Arguments) {
    # docker 的进度与错误都走 stderr；在 $ErrorActionPreference='Stop' 下 stderr
    # 会被包成 NativeCommandError 直接中断脚本，而命令其实是成功的。
    # 所以这里临时把偏好降为 Continue，只看退出码判断成败。
    #
    # 不走 `cmd /c "docker ..."`：那需要手动拼引号，凭据里出现 & 或 ^ 时会被 cmd
    # 二次解释。直接调用可执行文件由 PowerShell 负责转义，少一层可变因素。
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

if (-not (Test-Path $ComposeFile)) { Fail "compose file not found: $ComposeFile" }

try {
    Invoke-Docker @("version", "--format", "{{.Server.Version}}") | Out-Null
} catch {
    Fail "Docker engine is not reachable. Start Docker Desktop and retry.`n$_"
}

$settings = Read-DotEnv (Join-Path $RepoRoot ".env")
$Database = if ($settings["MYSQL_DATABASE"]) { $settings["MYSQL_DATABASE"] } else { "eino" }
$DbUser = $settings["MYSQL_USER"]
$DbPassword = $settings["MYSQL_PASSWORD"]
if ([string]::IsNullOrWhiteSpace($DbUser) -or [string]::IsNullOrWhiteSpace($DbPassword)) {
    Fail "MYSQL_USER / MYSQL_PASSWORD must be set in .env"
}

# 容器名不硬编码：项目名由目录名推导，改目录名就会变。问 compose 拿实际 id。
$containerId = (Invoke-Docker @("compose", "-f", $ComposeFile, "ps", "-q", "mysql")) | Select-Object -First 1
if ([string]::IsNullOrWhiteSpace($containerId)) {
    Fail "mysql container is not running. Run 'make infra-up' first."
}
$containerId = $containerId.Trim()

$timestamp = Get-Date -Format "yyyyMMdd-HHmmss"
$target = Join-Path $BackupRoot $timestamp
New-Item -ItemType Directory -Path $target -Force | Out-Null
Write-Step "target directory: $target"

$artifacts = @()
$manifest = New-Object System.Collections.Generic.List[string]
$manifest.Add("backup_time_local: " + (Get-Date -Format "yyyy-MM-dd HH:mm:ss zzz"))
$manifest.Add("backup_time_utc: " + (Get-Date).ToUniversalTime().ToString("yyyy-MM-dd HH:mm:ssZ"))
$manifest.Add("database: $Database")
$manifest.Add("dump_format: mysqldump without --databases; restore into an existing empty database")
$manifest.Add("mysql_container: $containerId")

# mysqldump 在容器内写文件再 docker cp 出来。
# 不直接 `docker exec ... > file`：PowerShell 的重定向会按文本编码处理字节流，
# SQL dump 里的非 UTF-8 字节会被改写，恢复时才发现坏了。
Write-Step "dumping MySQL database $Database"
$dumpContainerPath = "/tmp/eino-backup-$timestamp.sql"
try {
    # 刻意不加 --databases：加上它，dump 里会带 `CREATE DATABASE` 与 `USE`，
    # 恢复演练就没法把同一份 dump 导进 eino_restore_check —— 那些语句会把
    # 数据写回生产库。不带它，dump 只含表结构与数据，落到哪个库由 -D 决定。
    # 代价是灾难恢复时要先手工 `CREATE DATABASE`，已写进 RUNBOOK。
    Invoke-Docker @("exec",
        "-e", "MYSQL_PWD=$DbPassword",
        $containerId, "mysqldump",
        "--single-transaction", "--quick", "--routines", "--events",
        "--default-character-set=utf8mb4",
        "-u", $DbUser,
        "--result-file=$dumpContainerPath",
        $Database) | Out-Null
    Invoke-Docker @("cp", "${containerId}:$dumpContainerPath", (Join-Path $target "mysql.sql")) | Out-Null
    Invoke-Docker @("exec", $containerId, "rm", "-f", $dumpContainerPath) | Out-Null
} catch {
    Fail "mysqldump failed: $_"
}
$artifacts += "mysql.sql"

# 表行数摘要：恢复演练就是拿这份数字比对的，所以必须是精确 COUNT(*)
# 而不是 information_schema.table_rows（InnoDB 下那是估算值，比对会误报）。
Write-Step "collecting exact row counts"
$tableList = (Invoke-Docker @("exec",
        "-e", "MYSQL_PWD=$DbPassword",
        $containerId, "mysql", "-N", "-B", "-u", $DbUser, "-e",
        "SELECT table_name FROM information_schema.tables WHERE table_schema='$Database' AND table_type='BASE TABLE' ORDER BY table_name")) |
    Where-Object { -not [string]::IsNullOrWhiteSpace($_) } |
    ForEach-Object { $_.Trim() }

if ($tableList.Count -eq 0) {
    Fail "database '$Database' has no base tables; refusing to record an empty backup"
}
$manifest.Add("")
$manifest.Add("[mysql_row_counts]")
foreach ($table in $tableList) {
    $count = (Invoke-Docker @("exec",
            "-e", "MYSQL_PWD=$DbPassword",
            $containerId, "mysql", "-N", "-B", "-u", $DbUser, "-D", $Database, "-e",
            "SELECT COUNT(*) FROM ``$table``")) | Select-Object -First 1
    $manifest.Add(("$table = " + $count.Trim()))
}

if (-not $SkipVolumes) {
    # 卷名也不硬编码：compose 会给非 external 卷加项目名前缀（<项目名>_milvus-data），
    # external 卷则用它自己的 name。前缀随目录名变化，所以按后缀匹配。
    Write-Step "resolving Milvus volumes"
    $allVolumes = (Invoke-Docker @("volume", "ls", "--format", "{{.Name}}")) |
        Where-Object { -not [string]::IsNullOrWhiteSpace($_) } |
        ForEach-Object { $_.Trim() }

    $wanted = @("milvus-data", "milvus-etcd", "milvus-minio")
    $resolved = @()
    foreach ($logical in $wanted) {
        $match = $allVolumes | Where-Object { $_ -eq $logical -or $_ -like "*_$logical" } | Select-Object -First 1
        if ($match) {
            $resolved += , @($logical, $match)
        } else {
            Write-Step "WARNING: volume for '$logical' not found; skipping"
        }
    }
    if ($resolved.Count -eq 0) {
        Write-Step "WARNING: no Milvus volumes found; this backup covers MySQL only"
    }

    foreach ($pair in $resolved) {
        $logical = $pair[0]
        $actual = $pair[1]
        $archive = "$actual.tgz"
        Write-Step "archiving volume $actual -> $archive"
        try {
            # :ro 只读挂载源卷：备份过程绝不能写业务数据卷。
            Invoke-Docker @("run", "--rm",
                "-v", "${actual}:/data:ro",
                "-v", "${target}:/backup",
                "alpine", "tar", "czf", "/backup/$archive", "-C", "/data", ".") | Out-Null
        } catch {
            Fail "volume archive failed for ${actual}: $_"
        }
        $artifacts += $archive
    }
}

# 校验和放在写 manifest 之前算：manifest 自己不需要自己的哈希。
Write-Step "computing SHA256"
$manifest.Add("")
$manifest.Add("[files]")
foreach ($name in $artifacts) {
    $path = Join-Path $target $name
    if (-not (Test-Path $path)) { Fail "expected artifact missing: $name" }
    $hash = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLower()
    $size = (Get-Item -LiteralPath $path).Length
    $manifest.Add("$name sha256=$hash bytes=$size")
}

$manifestPath = Join-Path $target "manifest.txt"
Set-Content -LiteralPath $manifestPath -Value $manifest -Encoding UTF8
Write-Step "manifest written: $manifestPath"

# 保留策略：按目录名排序（名字就是时间戳，字典序等于时间序），删除超出的最旧几份。
# 删除前逐个打印：清理是不可逆的，日志里必须留下删了什么。
if ($Keep -gt 0 -and (Test-Path $BackupRoot)) {
    $dirs = Get-ChildItem -LiteralPath $BackupRoot -Directory |
        Where-Object { $_.Name -match '^\d{8}-\d{6}$' } |
        Sort-Object Name -Descending
    if ($dirs.Count -gt $Keep) {
        Write-Step "retention: keep newest $Keep of $($dirs.Count)"
        foreach ($old in $dirs[$Keep..($dirs.Count - 1)]) {
            Write-Step "removing old backup: $($old.FullName)"
            Remove-Item -LiteralPath $old.FullName -Recurse -Force
        }
    }
}

Write-Step "done: $target ($($artifacts.Count) artifact(s))"
exit 0
