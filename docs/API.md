# HTTP / WebSocket API

启动：

```powershell
go run ./cmd/server -addr 127.0.0.1:18181
```

（`make run` 等价于此命令。）端口用 18181 是为了避开容器模式占用的宿主 18180，
详见 `COMMANDS.md` 第八节。

浏览器访问 `http://localhost:18181` 即可打开内置 Web 对话界面。前端资源已经
嵌入 Go 程序，不需要安装 Node.js 或单独启动开发服务器。

主要接口：

- `GET /health`：服务健康状态，返回体含 `debug` 字段，供前端判断是否显示技能入口
- `GET /skills`：可用 Skill。**仅 `debug: true` 时注册**，否则返回 404
- `POST /chat`：非流式聊天，JSON 字段为 `session_id`、`query`、`skill`。`skill` 仅在 `debug: true` 时生效，非调试模式一律忽略并按问题自主路由
- `GET /sessions`：会话列表
- `GET /sessions/{id}`：会话消息
- `GET /sessions/{id}/execution`：执行记录，参数 `run_id`、`after_sequence`、`limit`
- `DELETE /sessions/{id}`：删除会话、Checkpoint 及执行记录
- `POST /sessions/{id}/approval`：提交 `{"approved":true}` 并恢复任务
- `GET /ws?session_id={id}`：WebSocket 增量聊天
- `GET /metrics`：查看累计请求数和当前执行中的请求数

自动记忆接口（启用 `memory.enabled` 后可用）：

- `GET /memory/facts`：查看当前用户/项目范围内生效的长期记忆、版本、来源和索引状态。
- `GET /memory/turns/{turn_id}`：查看某轮记忆提取状态及自动判定原因。
- `DELETE /memory/facts/{id}`：撤销记忆并异步清理对应向量。
- `POST /memory/jobs/{id}/retry`：将失败任务幂等地重新排队。

评测与诊断：

```powershell
go run ./cmd/retrieve "问题"       # 查看 Milvus 原始分数
go run ./cmd/eval                  # 输出正确率、引用率、拒答率和平均耗时
.\scripts\test-integration.ps1    # 真实 Ollama/Milvus 集成测试
$env:RUN_RAG_EVAL = "1"           # 脚本同时运行固定 RAG 问答评测
.\scripts\test-integration.ps1
```

`cmd/eval` 默认要求正确率至少 `0.8`，引用率和拒答率均为 `1.0`；未达到时返回
非零退出码。可用 `-min-correct`、`-min-citation`、`-min-refusal` 调整门槛。

普通 `go test -p 1 ./...` 不会访问 Ollama/Milvus。当前机器内存较紧时使用
`-p 1` 串行编译；外部依赖测试只有设置 `RUN_E2E=1` 或执行上述脚本才运行。

2026-09-09 本地验收结果：固定 3 题的正确率、引用率、拒答率均为 `1.0`，
平均耗时为 `4360ms`。该结果用于确认链路可用，不代替扩大业务评测集。

WebSocket 连接后先收到 `ready`，发送与 `/chat` 相同的 JSON；服务依次发送
`chunk`、`approval`、`done` 或 `error` 事件。

## 实时执行进度（P8）

由 `execution_events.enabled` 控制，默认关闭；关闭时协议与上面完全一致。
开启后：

- 正文仍为 `chunk` 帧，额外带 `run_id` 与 `sequence`；`sequence` 由同一计数器
  分配，正文与执行事件共用，保证顺序稳定。
- 执行事件为独立 `event` 帧，携带 `version`、`event_id`、`run_id`、`session_id`、
  `sequence`、`occurred_at`、`type`、`summary` 和结构化 `payload`。
  `type` 取值：`run_started`、`model_waiting`、`tool_started`、`tool_completed`、
  `tool_failed`、`skill_preloaded`、`skill_loaded`、`file_read_completed`、
  `approval_required`、`run_completed`、`run_failed`、`run_cancelled`。
  `summary` 仅供展示，断言与入库一律使用 `payload` 字段。
- 客户端可发送 `{"type":"cancel"}` 取消当前轮次；服务回 `{"type":"cancelled"}`。
- 背压：出站队列默认 256，队列满时中间事件可丢弃（帧带 `dropped` 标记），
  客户端用 `/sessions/{id}/execution?after_sequence=N` 补取；三个终态事件必须送达。
- `POST /chat` 与 `POST /sessions/{id}/approval` 为同步接口，返回体中
  `events` 字段内联本轮事件数组；实时推送仅由 WebSocket 提供。
- 刷新页面时按 `run_id` 回放历史事件：`GET /sessions/{id}/execution` 返回
  `runs`（含 `assistant_sequence`）与 `events`，据此把事件挂回对应助手消息。


