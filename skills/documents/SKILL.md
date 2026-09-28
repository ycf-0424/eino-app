---
scenarios: ["读取和总结授权目录中的文本或 DOCX 工单；提取文档正文信息", "按用户明确要求修改 DOCX 并保存为新文件"]
not_for: ["覆盖原 Word 文件", "PDF、Excel 或演示文稿文件处理"]
description: 读取授权目录中的文本与 DOCX；用户明确要求时可通过审批后的 document_write 生成新的 DOCX
required_tools: [local_file_read, document_write]
---

# 文档处理

仅处理用户明确指定的文件。读取请求如果本轮上下文已包含服务端注入的授权文件正文，直接依据正文回答，不要重复读取。

普通读取、总结和提取使用 `local_file_read`。用户明确要求修改 Word/DOCX 时，使用 `document_write`：

- `source_path` 必须是授权目录内的 `.docx` 文件；
- 只提交用户明确要求的文字替换或追加段落；
- `output_name` 必须是新的 `.docx` 文件名，不能覆盖源文件；
- 工具会先请求人工审批，审批前不会写入；
- 当前只支持单个 Word 文本运行内的替换和文档末尾追加段落，不承诺复杂排版、批注、修订或跨运行替换。

如果没有明确的修改动作，不能调用 `document_write`，也不能声称文件已经改变。
