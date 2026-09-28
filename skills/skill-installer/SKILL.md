---
scenarios: ["从公开 GitHub 仓库安装技能"]
not_for: ["没有仓库路径时猜测来源", "安装未知来源或未允许域名的压缩包"]
description: 从管理员允许的公开 GitHub 仓库下载、校验并通过审批安装项目技能
required_tools: [skill_install]
---

# 技能安装

安装前必须取得公开 GitHub 的 `owner/repository`、仓库内技能目录 `path`，并尽量指定不可变的 tag 或 commit `ref`。不猜测仓库来源，也不执行技能包中的脚本。

调用 `skill_install` 后，服务端会：

- 只从配置允许的下载域名获取 ZIP；
- 限制压缩包大小、文件数量和路径穿越；
- 要求包内存在合法的 `SKILL.md`；
- 默认不覆盖同名项目技能；
- 写入前请求人工审批，审批前不能声称技能已安装。

当前实现面向公开 GitHub 仓库，不提供私有仓库凭据或 Git fallback。安装完成后，技能会在后续请求的技能加载目录中可用。
