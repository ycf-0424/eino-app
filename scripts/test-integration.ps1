$ErrorActionPreference = "Stop"
$env:RUN_E2E = "1"
# 本机内存有限时串行编译，避免 Milvus SDK 等大型依赖并行链接导致 OOM。
go test -tags=integration -p 1 ./internal/integration -v -count=1
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

# 可选执行完整生成评测（会连续调用本地大模型，耗时更长）。
if ($env:RUN_RAG_EVAL -eq "1") {
    go run ./cmd/eval
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}
