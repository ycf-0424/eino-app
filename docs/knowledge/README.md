# 示例知识库

把需要检索的文档放在本目录，然后执行：

```powershell
go run ./cmd/indexer
```

当前支持：

- Markdown（按标题建立逻辑段）
- TXT
- HTML / HTM（忽略 script 和 style）
- JSON（校验并格式化后入库）
- DOCX（读取正文段落）
- PDF（逐页提取文字并保留页码）

程序会按 `config.yaml` 切分文档，使用 Ollama `bge-m3` 生成向量，并写入
`rag.store` 指定的 Redis Stack 或 Milvus。

`.index-manifest.json` 保存每个文档块的 SHA-256。再次运行时只向量化新增或变化的块，
文件删除后也会从向量库清理对应旧块。不要手动编辑清单；需要全量重建时，应同时清空
目标 Collection/索引和该清单，避免旧向量残留。
