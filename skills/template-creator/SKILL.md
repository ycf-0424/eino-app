---
scenarios: ["根据授权目录中的参考文件制作可复用模板", "更新已有项目模板"]
not_for: ["一次性内容生成", "没有用户明确授权时修改或覆盖现有模板"]
description: 根据授权目录中的参考文件创建项目内可复用模板，并通过审批后保存模板包
required_tools: [template_write]
---

# 模板创建

模板必须来自用户明确指定、且位于管理员授权目录中的参考文件。先确认模板名称、用途和文件类型，再调用 `template_write`。

支持的参考类型：DOCX、XLSX/XLS/CSV、PPTX/PPT、PNG/JPG、TXT（email 或 slack）。工具会复制参考文件，生成 `SKILL.md` 和 `artifact-template.json`，保存到项目的模板目录；原始参考文件不会被修改。

规则：

- 新模板默认使用新名称，不覆盖已有模板；只有用户明确要求更新时才设置 `overwrite=true`。
- `reference_path` 和可选的 `preview_path` 必须位于授权目录内。
- 写入前会请求人工审批；审批前不能声称模板已经保存。
- 不要使用不存在的 Codex 脚本、`$CODEX_HOME` 或外部模板市场；本项目模板由服务端工具落盘。
