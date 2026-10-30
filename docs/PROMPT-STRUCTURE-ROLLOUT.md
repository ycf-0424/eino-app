# Prompt 结构化落地记录

本次改造保留现有 Eino Agent、RAG、Skill、审批和工具中间件，只增加可回滚的 Prompt 组装层。

## 配置开关

```yaml
prompt:
  profile: legacy # legacy、v2-shadow 或 v2
```

默认值是 `legacy`。生产环境先保持 legacy；`v2-shadow` 会继续向模型发送 legacy Prompt，但同时计算并记录 v2 哈希；对比工具调用率、命中率、延迟、失败率和人工纠正率后再切换到 `v2`。

## 已落地能力

- `internal/eino/prompt/registry.go` 提供有序 Section、角色 Prompt、工具专属指导、动态上下文包装、版本和 SHA-256 哈希。
- Supervisor、Knowledge Agent、Writer Agent 均可按 profile 渲染；旧构造器继续使用 legacy。
- `knowledge_search`、`local_file_read`、`current_time`、`write_note` 已有独立使用、禁止、证据和审批说明。
- Skill、服务端预检索和长期记忆分别进入 `<skill_context>`、`<runtime_context>`、`<memory_context>`。
- 执行事件新增 `prompt_assembled`，只记录 profile、Agent 角色、Prompt 版本、Prompt 哈希、工具集合哈希、上下文快照摘要和哈希，不写入私有正文。
- 模型适配层新增 `model_request` / `model_completed` 事件；每次真实 Generate/Stream 尝试记录 attempt、输入摘要、Prompt/工具/上下文哈希和错误摘要，流式结束记录 stop reason。
- 工具开始和终态事件现在带稳定的 `turn_id`、独立 `step_id` 和 `attempt_id`；并发工具不会共享同一个工具调用作用域。
- 审批恢复沿用原 turn，创建新的 step/attempt，便于区分原始中断和恢复尝试。
- 执行事件信封新增可选的 `turn_id`、`step_id`、`attempt_id`，现有 `run_id + sequence` 查询和旧事件仍兼容。
- Prompt 快照位于 `internal/eino/prompt/testdata/`，用于检测模型可见文本的意外变化。
- `RegisterToolGuidance` 为后续 MCP 工具注册专属提示词保留扩展点。

## 回滚方式

把配置改回：

```yaml
prompt:
  profile: legacy
```

该回滚不需要重新索引知识库，不会修改会话、向量库或工具权限代码。

## 后续接入 MCP

MCP Client 接入时，每个远程工具先转换为现有 `einotool.BaseTool`，再复用 Owner 隔离、审批、超时、错误降级和执行事件。连接建立后可调用 `prompt.RegisterToolGuidance` 注册工具规则；MCP Server 返回的 instructions 只能作为运行时引用上下文，不能覆盖系统规则。

## 第二、三阶段边界

当前已完成的是不改数据库结构的增量版本：角色/工具/上下文规则、Prompt 审计、模型请求尝试和事件层级已接入现有执行日志。完整的 `agent_turns`、`agent_steps`、`agent_attempts` 独立表、受控正文快照和回放 UI 仍应在真实运行数据验证保留策略后再单独迁移；现有 `run_id + sequence` 回放接口继续作为兼容入口。

断点恢复最小版本已启用：启动清理会保留有审批票据的运行；无审批的 `interrupted` 运行可通过 `POST /sessions/{id}/resume` 从 Eino checkpoint 继续。恢复前会比较 Prompt、工具集合和运行上下文摘要，能力不一致时拒绝恢复；恢复使用新 run/step/attempt，原运行记录保持只读审计。
