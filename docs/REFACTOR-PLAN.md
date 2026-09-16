# eino 框架收敛 与 技能路由质量 改造方案

> ⚠️ **本文已归档为历史记录，不再是执行依据。**
> 合并后的**唯一执行来源**是 `docs/EXECUTION-PLAN.md` —— 它把本文的 P-REFACTOR 阶段 0–4、P-SKILL-QUALITY 的 S1–S8、`AUTH-PLAN.md` 的全部内容与三条上线缺口重排为 **28 个连续步骤**，并预置了全部 11 条待决策事项。
> 本文保留价值：证据链、现状核实、设计动因、以及各步骤的详细出处。**执行时请以 EXECUTION-PLAN 为准。**
> 已知本文与最终方案的两处差异：① 第七节决策 3 建议拆出 `observability.Redact`，最终**不拆**；② 本文「文档元信息」写「AUTH-PLAN 最先」，而第十七节的统一顺序是「S1–S6 最先、AUTH-PLAN 次之」，最终方案采纳后者。

> **合并说明**：本文合并两份独立方案 —— **P-REFACTOR**（eino 命名空间收敛）与 **P-SKILL-QUALITY**（技能路由质量）。
> 第一部分完整保留 P-REFACTOR 原有章节与编号（其内部交叉引用如「第六节阶段 4」「第七节决策点 4」仍然有效）；第二部分为新增。
> 第三部分给出与 AUTH-PLAN 的**三方统一实施顺序**、文件级交集分析与尚未归属方案的缺口。
> 所有行号与统计数字均基于改造前的代码实测，可直接跳转核对。

---

## 文档元信息

| 项 | 内容 |
|---|---|
| 方案编号 | 第一部分 `P-REFACTOR`；第二部分 `P-SKILL-QUALITY`（建议合并时均沿用） |
| 性质 | P-REFACTOR：结构重构，**零行为变化**；P-SKILL-QUALITY：元数据修正 + 可测性增强（其中 S1–S4 为零行为变化的文本修正） |
| 前置依赖 | P-REFACTOR：git 版本控制（阶段 0）。P-SKILL-QUALITY：**S7 / S8 必须在 P-REFACTOR 阶段 1 之后**（理由见第十四节与第十八节） |
| 与 AUTH-PLAN 的顺序 | **AUTH-PLAN 最先**；本方案 S1–S6 可与 AUTH-PLAN 并行；P-REFACTOR 在三者中最后 |
| 是否阻塞上线 | **均不阻塞。** 两份都是质量改造，不是缺陷修复；真正的上线阻塞项在 AUTH-PLAN |
| 回滚方式 | P-REFACTOR 阶段 1 单次 commit revert；P-SKILL-QUALITY 按 S 独立提交，逐项 revert |

---

# 第一部分　eino 框架收敛改造（P-REFACTOR）

> 目标：把与 eino 打交道的代码集中到 `internal/eino/` 一个命名空间下，使「谁依赖 eino」一眼可见，为将来 eino 升级或更换框架留出可预期的改动范围。

## 一、目标与非目标

### 1.1 目标

让「谁依赖 eino」一眼可见：新增或调整 eino 相关代码时，路径可预测；将来升级 eino 版本或更换框架时，改动范围收敛在一个父包内。

### 1.2 关键概念澄清：这是一个「命名空间」，不是一个「层」

需要先说清一件事，否则验收标准会定错：

**收敛后的 `internal/eino/` 不是最底层，它仍然依赖 `execution` / `session` / `output` / `config`。**

实测依赖方向（非测试代码）：

| 收敛后的包 | 仍会依赖 |
|---|---|
| `internal/eino/agent` | `execution`（25 行）、`session`（1 行） |
| `internal/eino/chain` | `output`（5 行） |
| `internal/eino/model`、`internal/eino/rag` | `config` |

其中 `internal/config` 本身零 eino 依赖、零项目内依赖，`eino → config` 是正常方向，不是问题。

所以本方案的验收标准是「**eino 代码集中在可预测的位置**」，**不是**「eino 层零反向依赖」。后者是另一件事（依赖倒置重构），代价高得多，见第七节决策点 4。

### 1.3 非目标

- 不改任何 eino API 调用写法（签名、调用序列一律不动）
- 不改任何运行时行为、不新增功能
- 不做依赖倒置（不切断 `agent → execution` 等反向依赖）
- 不引入 `internal/bootstrap/` 之类的装配层（那是独立议题）

---

## 二、现状核实

### 2.1 好消息：收敛的物理条件已经具备

**`compose` / `adk` / `callbacks` 这三个「编排类」子包，全项目只出现在 5 个文件里，且全部在待收敛集内：**

| 文件 | 引用行数 |
|---|---|
| `internal/agent/agent.go` | 3 |
| `internal/agent/middleware.go` | 1 |
| `internal/agent/middleware_test.go` | （测试） |
| `internal/chain/rag_chain.go` | 1 |
| `internal/observability/debug.go` | 1 |

**项目其余部分（`server`、`memory`、`skill`、`execution`、`session`、`cmd/*` 等）对编排 API 的引用为零。**

这意味着：eino 的「机制性依赖」本来就没有扩散出去，收敛是一次纯机械的路径整理，不需要先做任何解耦。

### 2.2 渗漏的性质要分开看：两类，不是一类

| 依赖的子包 | 性质 | 是否算污染 |
|---|---|---|
| `cloudwego/eino/schema`（Message / Role） | 跨层共用的**数据类型** | **不算**。这是数据契约，`session` / `output` / `memory/mysql.go` / `server/skill_catalog.go` 属于此类 |
| `cloudwego/eino/compose`、`adk`、`callbacks` | **编排机制** | 算，但已全部在集内（见 2.1） |
| `cloudwego/eino/components/*`、`eino-ext/components/*` | **组件构造** | 算。集外还有 6 个非测试文件仍在构造组件 |

所以要处理的是第三类。集外仍在直接接触 `components/*` 的非测试文件：

| 文件 | 用到的 eino 子包 | 性质 |
|---|---|---|
| `internal/memory/coordinator.go:6-7` | `eino-ext/components/embedding/openai`、`components/model` | ⚠️ **直接构造 embedder** |
| `internal/memory/extractor.go:7-8` | `components/model`、`schema` | 类型（持有 chat model 字段） |
| `internal/memory/milvus.go:7` | `components/embedding` | 类型（Embedder 接口） |
| `internal/server/service.go:16-20` | `components/model`、`components/tool`、`schema` | 类型（字段与签名） |
| `internal/skill/runtime.go:9-11` | `components/tool`、`components/tool/utils`、`schema` | ⚠️ **构造工具**（`utils.NewTool`） |
| `cmd/console/main.go` | `components/tool`、`schema` | 类型 |

**关键区分**：`memory/coordinator.go` 和 `skill/runtime.go` 是**真构造**（`NewEmbedder` / `utils.NewTool`），其余 4 个只是持有 eino 类型（字段、参数、返回值）——类型依赖无法也不该消灭，构造可以下沉。

### 2.3 一个被忽略的重复构造

同一个 embedder 被构造了两次：

- `internal/rag/factory.go:22-25` —— `openaiembedding.NewEmbedder(...)`
- `internal/memory/coordinator.go:32-34` —— `openaiembedding.NewEmbedder(...)` 同一份配置

`rag/factory.go` 的注释写着「Redis 和 Milvus 共用同一个 Embedding 实例，确保入库和检索向量一致」，但 memory 侧又独立构造了一份。这是收敛时顺手可以消除的重复。

### 2.4 改造规模（实测）

| 项 | 数量 |
|---|---|
| 需要 `git mv` 的 .go 文件 | **37** |
| 需要改写的 import 行 | **29**（分布在 15 个文件） |
| 其中集内跨包引用 | 9 行 / 5 文件 |
| 其中集外引用 | 20 行 / 10 文件 |
| 需要改动的**调用点** | **0**（理由见下） |

### 2.5 为什么调用点是 0：别名已经存在

现有代码已经为内部包起了别名，用来避开与 eino 官方包的重名：

```
modelset  "my-eino-app/internal/model"
toolset   "my-eino-app/internal/tool"
appserver "my-eino-app/internal/server"
```

路径从 `internal/model` 变成 `internal/eino/model` 时，**别名不用变，包名也不变**（`internal/eino/model` 的包名仍是 `model`），因此所有调用点（`modelset.NewChatModel(...)`、`toolset.NewXxxTool(...)`）**一个字都不用改**。只有 import 行里的路径字符串需要改。

这是本次改造风险低的核心原因，也是必须保留别名约定的原因。

### 2.6 顺带发现：`agent.go` 的 import 分组错位

`internal/agent/agent.go:9` 把 `"my-eino-app/internal/session"` 放在了**标准库组内**（夹在 `"io"` 和 `"os"` 之间）：

```go
import (
	"context"
	"errors"
	"fmt"
	"io"
	"my-eino-app/internal/session"   // ← 错位
	"os"
	...
)
```

`gofmt` 只在组内排序，不会跨组移动，所以 `make fmt` 一直没报错。阶段 1 顺手修正，风险为零。

---

## 三、目标布局

```
internal/
├── eino/                          ← 新增父包：所有直接对接 eino 的代码收于此处
│   ├── agent/                     (← internal/agent)          4 文件
│   ├── chain/                     (← internal/chain)          3 文件
│   ├── model/                     (← internal/model)          3 文件
│   ├── prompt/                    (← internal/prompt)         2 文件
│   ├── rag/                       (← internal/rag)           13 文件
│   ├── tool/                      (← internal/tool)           9 文件
│   └── observability/             (← internal/observability)  3 文件
│
├── execution/    session/    memory/    skill/    server/        ← 位置不变
├── checkpoint/   config/     evaluation/ integration/ health/
└── output/                                                      ← 位置不变（仅依赖 schema）
```

### 3.1 迁移映射表

| 现路径 | 新路径 | 依据 |
|---|---|---|
| `internal/agent` | `internal/eino/agent` | 编排 eino Runner / ADK Agent，import `adk`+`compose` |
| `internal/chain` | `internal/eino/chain` | compose 图编排 |
| `internal/model` | `internal/eino/model` | 导出 eino 类型，构造 chat model |
| `internal/prompt` | `internal/eino/prompt` | 导出 eino ChatTemplate |
| `internal/rag` | `internal/eino/rag` | 构造 embedder / 包装 eino-ext indexer+retriever |
| `internal/tool` | `internal/eino/tool` | 导出类型即 `einotool.InvokableTool` |
| `internal/observability` | `internal/eino/observability` | 注册 eino callbacks Handler |
| `internal/session` | **不动** | 仅用 `schema`（数据类型） |
| `internal/output` | **不动** | 仅用 `schema`（数据类型） |
| `internal/memory` | **不动** | 见 3.2 |
| `internal/skill` | **不动** | 见 3.2 |
| `internal/server` | **不动** | 见 3.2 |

### 3.2 `memory` / `skill` / `server` 为什么不搬

它们的 eino 依赖是**类型级**的（字段、参数、返回值），不是构造级或编排级的。搬进来会把这些业务包整个吃掉，违背收敛的初衷。正确做法是让 eino 层暴露构造函数，它们只调构造函数——见第六节阶段 3。

---

## 四、逐项改造清单（阶段 1：29 行 import）

### 4.1 集外文件（10 个文件，20 行）

| 文件:行 | 现路径 | 改为 |
|---|---|---|
| `cmd/console/main.go:23` | `internal/agent` | `internal/eino/agent` |
| `cmd/console/main.go:24` | `internal/chain` | `internal/eino/chain` |
| `cmd/console/main.go:28` | `internal/model` | `internal/eino/model` |
| `cmd/console/main.go:29` | `internal/observability` | `internal/eino/observability` |
| `cmd/console/main.go:30` | `internal/rag` | `internal/eino/rag` |
| `cmd/console/main.go:34` | `toolset "internal/tool"` | `toolset "internal/eino/tool"` |
| `cmd/eval/main.go:14` | `internal/chain` | `internal/eino/chain` |
| `cmd/eval/main.go:18` | `internal/model` | `internal/eino/model` |
| `cmd/eval/main.go:19` | `internal/rag` | `internal/eino/rag` |
| `cmd/indexer/main.go:13` | `internal/rag` | `internal/eino/rag` |
| `cmd/memory-reindex/main.go:9` | `modelset "internal/model"` | `modelset "internal/eino/model"` |
| `cmd/retrieve/main.go:14` | `internal/rag` | `internal/eino/rag` |
| `cmd/server/main.go:15` | `internal/observability` | `internal/eino/observability` |
| `internal/evaluation/evaluation.go:12` | `internal/chain` | `internal/eino/chain` |
| `internal/integration/integration_test.go:14` | `internal/rag` | `internal/eino/rag` |
| `internal/memory/live_test.go:7` | `modelset "internal/model"` | `modelset "internal/eino/model"` |
| `internal/server/service.go:21` | `internal/agent` | `internal/eino/agent` |
| `internal/server/service.go:26` | `modelset "internal/model"` | `modelset "internal/eino/model"` |
| `internal/server/service.go:27` | `internal/rag` | `internal/eino/rag` |
| `internal/server/service.go:30` | `toolset "internal/tool"` | `toolset "internal/eino/tool"` |

⚠️ **三个测试文件必须一起改**（`middleware_test.go`、`live_test.go`、`integration_test.go`）。漏改时 `go build ./...` 仍然通过，只有 `go test ./...` 才会报错。

### 4.2 集内文件（5 个文件，9 行）

包整体移动后，包与包之间的相对引用也要改路径：

| 文件:行 | 现路径 | 改为 |
|---|---|---|
| `internal/agent/agent.go:25` | `internal/observability` | `internal/eino/observability` |
| `internal/agent/agent.go:26` | `internal/prompt` | `internal/eino/prompt` |
| `internal/agent/agent.go:27` | `toolset "internal/tool"` | `toolset "internal/eino/tool"` |
| `internal/agent/middleware.go:14` | `internal/observability` | `internal/eino/observability` |
| `internal/agent/middleware.go:15` | `toolset "internal/tool"` | `toolset "internal/eino/tool"` |
| `internal/agent/middleware_test.go:11` | `toolset "internal/tool"` | `toolset "internal/eino/tool"` |
| `internal/chain/rag_chain.go:14` | `internal/prompt` | `internal/eino/prompt` |
| `internal/chain/rag_chain.go:15` | `internal/rag` | `internal/eino/rag` |
| `internal/tool/knowledge.go:14` | `internal/rag` | `internal/eino/rag` |

### 4.3 不需要改的

- 所有 `cloudwego/eino/*` 的 import（那是外部依赖，位置不变）
- 所有调用点（别名不变，见 2.5）
- `Makefile`、`Dockerfile`、`docker-compose*.yml`、`scripts/`、`docs/` —— 实测**没有任何非 Go 文件引用这 7 个包的路径**
- 环境变量、配置文件、前端 —— 完全不涉及

### 4.4 建议的执行方式

一次提交完成（Go 不支持部分重命名，中间状态必然编译不过）：

```bash
cd E:/11/my-eino-app
mkdir -p internal/eino
for p in agent chain model prompt rag tool observability; do git mv internal/$p internal/eino/$p; done
# 再改 29 行 import 路径（用编辑器全局替换，注意只替换引号内的路径）
go build ./... && go vet ./... && go test -p 1 ./...
```

替换时注意 `internal/model` 与 `internal/eino/model` 的前缀关系：**先替换更长的路径，或直接在引号内匹配 `"my-eino-app/internal/model"` 全串**，避免把已替换的结果二次替换成 `internal/eino/eino/model`。

---

## 五、必须处理的坑

| # | 坑 | 后果 | 应对 |
|---|---|---|---|
| 1 | **`go test` 不能用默认并行度** | 本机内存不足导致编译失败（`Makefile` 的 test 目标注释已写明「串行编译避免本机内存不足」） | 一律用 `go test -p 1 ./...` |
| 2 | **测试文件的 import 容易漏改** | `go build ./...` 全绿，`go test ./...` 挂 | 29 行清单里 3 行在测试文件，逐个核对 |
| 3 | **路径前缀二次替换** | `internal/eino/eino/model` | 匹配引号内完整串，或先替换长路径 |
| 4 | **`internal/eino` 是普通包名 `eino`** | 若某文件同时需要 `github.com/cloudwego/eino` 根包与门面包，需起别名 | 现状**没有任何文件导入 cloudwego/eino 根包**（只导入子包），暂无冲突；阶段 3 若引入门面包需留意 |
| 5 | **别名约定必须保住** | 一旦把 `modelset` / `toolset` 丢掉改成默认名，就会与 `einomodel` / `einotool` 混在一起，可读性崩掉 | 阶段 1 只改路径，**不动别名**；新代码沿用 `xxxset` 风格 |
| 6 | **别顺手改 eino API 写法** | 一旦混入 API 调整，就无法用「行为零变化」来验收 | 本方案严格限定为路径移动，API 调用一律不碰 |

---

## 六、实施顺序

分 4 个阶段，每阶段一个独立 commit，阶段 1 之后每一步都可以单独决定做不做。

### 阶段 0：基线（前置，必做）

项目**当前没有 git 仓库**（`.git` 不存在）。37 个文件移动 + 29 行 import 改写，没有版本控制无法回退。

```bash
cd E:/11/my-eino-app
git init
# 先确认 .gitignore 覆盖：data/、.env、eino-server、会话 JSON 等运行时产物
git add -A && git commit -m "baseline before eino convergence"
# 记录改造前基线
go build ./... && go vet ./... && go test -p 1 ./...
```

**把基线测试结果记下来**（通过数／失败数）。若基线本身就有失败用例，必须先区分「改造引入」与「改造前已存在」。

### 阶段 1：机械平移（核心，一次性）

- `git mv` 7 个目录（37 文件）到 `internal/eino/`
- 改 29 行 import 路径（15 个文件，明细见第四节）
- 顺手修正 `internal/agent/agent.go:9` 的 import 分组错位
- 验证：`go build ./...` && `go vet ./...` && `go test -p 1 ./...`
- 提交：`refactor: converge eino code into internal/eino namespace`

**这一阶段完成后，本方案的主要目标已经达成。** 后面的阶段都是可选增强。

### 阶段 2：边界守卫（建议做，成本极低）

加一个防回归的检查，否则这次收敛会在几个月内被新代码重新扩散掉。

`Makefile` 新增目标，并入 `check`：

```makefile
boundary: ## 校验 eino 编排 API 未扩散到 internal/eino 之外
	@! grep -rl "cloudwego/eino/\(compose\|adk\|callbacks\)" --include="*.go" internal/ cmd/ \
	  | grep -v "^internal/eino/" \
	  || (echo "❌ compose/adk/callbacks 出现在 internal/eino/ 之外" && exit 1)
	@echo "✅ eino 编排边界完好"

check: fmt boundary test vet
```

当前应通过（实测 5 个引用文件全在集内）。将来若有人在外层直接 `compose.NewGraph`，`make check` 会当场拦住。

### 阶段 3：消除构造泄漏（建议做，收益明确）

把「构造 eino 组件」这件事从业务包收回到 eino 层。四项独立，可分开做：

**3.1 embedder 构造去重（消除 2.3 的重复）**

新增 `internal/eino/embedding/embedding.go`：

```go
// NewFromConfig 按配置构造 Embedder。
// rag 与 memory 共用同一份构造逻辑，避免两处配置漂移导致向量空间不一致。
func NewFromConfig(ctx context.Context, cfg config.Embedding) (embedding.Embedder, error)
```

改造：
- `internal/eino/rag/factory.go:22-25` → 调 `embedding.NewFromConfig`
- `internal/memory/coordinator.go:32-34` → 调 `embedding.NewFromConfig`，memory 从此不再 import `eino-ext/components/embedding/openai`

**3.2 工具构造下沉**

`internal/skill/runtime.go:70` 的 `utils.NewTool(info, func...)` 是直接构造 eino 工具。
在 `internal/eino/tool` 暴露一个接收业务函数的构造函数（如 `toolset.NewReaderTool(fn)`），skill 只传函数，不再 import `components/tool/utils`。

**3.3 业务包改用门面**

新增 `internal/eino/facade.go`（类型别名，零运行时开销、不产生包装层）：

```go
package eino

import (
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/embedding"
)

// 类型别名：编译后与底层类型完全等价，不引入间接层。
type ChatModel = einomodel.ToolCallingChatModel
type BaseTool = einotool.BaseTool
type Embedder = embedding.Embedder
```

改造 `internal/server/service.go:16-17`、`internal/memory/extractor.go:7`、`internal/memory/milvus.go:7`、`cmd/console/main.go`，改为只 import `internal/eino`。

**阶段 3 完成后的效果**：`server` / `memory` / `skill` 三个业务包对 `cloudwego/eino/components/*` 的依赖降为 0（仅保留 `schema`）。

### 阶段 4（可选，不建议现在做）

切断 `internal/eino/` 的反向依赖。逐条评估：

| 反向依赖 | 规模 | 可行性 | 建议 |
|---|---|---|---|
| `agent → session`（`CompactHistory`） | 1 行 | 高，`session.CompactHistory` 是纯函数（入参 `[]*schema.Message`），可下沉到 eino 层 | 想做就做，成本低 |
| `chain → output`（`Answer` / `ParseModelAnswer`） | 5 行 | 中，`output` 只依赖 `schema`，把 `Answer` 定义在 chain 内即可 | 收益低，可不做 |
| `agent → execution`（`Emitter` / `Event` / `Annotate`） | 25 行 / 4 文件 | 低，需在 eino 层定义事件接口 + 外部适配器 | **不建议** |
| `model` / `rag → config` | — | — | **不应改**，`config` 是底层，这是正常方向 |

**为什么 `agent → execution` 不建议切**：agent 通过 Emitter 上报执行进度（模型等待、工具开始/结束、需要审批）不是「框架碰了业务」，而是**运行时职责本身**。强行倒置需要定义一层事件接口 + 适配器，引入间接层而换不来实际收益——25 行跨 4 文件的改动，风险高于收益。

---

## 七、待决策事项

| # | 决策 | 建议 | 理由 |
|---|---|---|---|
| 1 | `internal/tool` 整体移入，还是拆成「eino 适配」+「业务逻辑」 | **整体移入** | 9 个文件的导出类型就是 `einotool.InvokableTool`，拆开只会制造碎片。若将来工具数量增长到几十个再考虑 |
| 2 | `internal/rag` 整体移入，还是按「是否包装 eino 组件」拆 | **整体移入** | eino touchpoint 分散在 `factory.go`（构造 embedder）、`redis.go`（包装 eino-ext indexer/retriever）、`milvus.go`（Embedder 类型）。拆开会让「一半在外、一半在内」，反而更难解释 |
| 3 | `internal/observability` 是否把 `Redact` 拆出来 | **建议拆** | `debug.go` 注册 eino callbacks（属 eino 层），`redact.go` 是通用脱敏工具（零 eino 依赖）。把通用工具放进 eino 命名空间会让边界重新模糊。成本：移 2 个文件 + 5 处调用点改名 |
| 4 | 是否做阶段 3 | **建议做** | 消除重复构造（3.1）是实打实的收益；门面（3.3）成本极低 |
| 5 | 是否做阶段 4 | **不做** | 见第六节阶段 4 的逐条评估 |

---

## 八、与其他方案的关系

### 8.1 与 AUTH-PLAN 的顺序

**AUTH-PLAN（飞书登录 + 多用户隔离）在前，本方案在后。**

理由：
1. AUTH-PLAN 是上线阻塞项，本方案是结构优化，优先级不同；
2. AUTH-PLAN 全文带行号跳转，本方案会重写 29 行 import、移动 37 个文件，**先做本方案会让 AUTH-PLAN 的全部行号基准失效**；
3. 本方案的阶段 0 只需要 `git init`，而 `git init` 本来就是 AUTH-PLAN 阶段 1 的前置条件——**两者共用同一次 `git init`，不冲突**。

> 三方统一顺序（含第二部分 P-SKILL-QUALITY）见**第十七节**；本节结论与之不冲突，仅范围更窄。

### 8.2 交集只有 2 个文件

| 文件 | AUTH-PLAN 的改动 | 本方案的改动 |
|---|---|---|
| `internal/server/service.go` | owner 流转、9 处调用点 | 4 行 import 路径 |
| `cmd/server/main.go` | 装配 auth 模块 | 1 行 import 路径 |

其余零交集：`session` / `checkpoint` / `config` / `memory` 都不引用待收敛的 7 个包。

（含第二部分的**三方**交集表见第十八节。）

### 8.3 本方案为 AUTH-PLAN 预留的一条约束

**AUTH-PLAN 新增的 `internal/auth/` 不得 import `cloudwego/eino`。** 它是纯 HTTP + OAuth + context 传递，一旦引入 eino 依赖，就等于制造了一个新的待收敛包，本方案的边界守卫（阶段 2）将来会拦住它。

---

## 九、验收清单

### 编译与静态检查
- [ ] `go build ./...` 通过
- [ ] `go vet ./...` 通过
- [ ] `go test -p 1 ./...` 结果与基线一致（数量与通过数逐项对齐）

### 目录断言
- [ ] `internal/` 下不再存在 `agent` / `chain` / `model` / `prompt` / `rag` / `tool` / `observability` 这 7 个顶层目录
- [ ] `internal/eino/` 下存在这 7 个子包
- [ ] `internal/` 下仍有 `session` / `output` / `execution` / `memory` / `skill` / `server` / `config` / `checkpoint` / `evaluation` / `integration` / `health`

### 边界断言
- [ ] `internal/eino/` 之外无 `compose` / `adk` / `callbacks` 引用（阶段 2 的 `make boundary`）
- [ ] `internal/eino/` 之外仅剩 `schema`（及阶段 3 完成后归零的 `components/*`）

### 行为零变化
- [ ] `make run` 起服务，浏览器访问 `:18180` 完成一次问答
- [ ] `make retrieve` 返回的检索分数与改造前一致（这是最灵敏的行为回归探针）
- [ ] `make eval` 三项指标（正确率 / 引用率 / 拒答率）与改造前一致（需 Ollama + Milvus）
- [ ] `make index` 可正常重建索引（验证 rag 包的移动未影响 manifest 逻辑）

> **建议优先跑 `make retrieve`**：`internal/rag/manifest.go` 依据 `EmbeddingModel + Dimension` 判定索引有效性，如果 embedder 构造路径被意外改动，检索分数会立刻异常，比任何单元测试都灵敏。

---

## 附：P-REFACTOR 受影响文件清单

**新增（1）**
- `internal/eino/`（37 个 .go 文件由 7 个包整体平移）

**import 路径改写（15）**
- `internal/agent/agent.go`（3 行 + 1 处分组错位修正）
- `internal/agent/middleware.go`（2 行）
- `internal/agent/middleware_test.go`（1 行）
- `internal/chain/rag_chain.go`（2 行）
- `internal/tool/knowledge.go`（1 行）
- `internal/server/service.go`（4 行）
- `internal/evaluation/evaluation.go`（1 行）
- `internal/memory/live_test.go`（1 行）
- `internal/integration/integration_test.go`（1 行）
- `cmd/console/main.go`（6 行）
- `cmd/eval/main.go`（3 行）
- `cmd/server/main.go`（1 行）
- `cmd/indexer/main.go`（1 行）
- `cmd/retrieve/main.go`（1 行）
- `cmd/memory-reindex/main.go`（1 行）

**阶段 2 新增（1）**
- `Makefile`（`boundary` 目标 + 并入 `check`）

**阶段 3 涉及（约 8）**
- 新增 `internal/eino/embedding/embedding.go`、`internal/eino/facade.go`
- 改 `internal/eino/rag/factory.go`、`internal/memory/coordinator.go`、`internal/memory/extractor.go`、`internal/memory/milvus.go`、`internal/skill/runtime.go`、`internal/server/service.go`、`cmd/console/main.go`

**明确不动（9）**
- `internal/session`、`internal/output`、`internal/execution`、`internal/checkpoint`、`internal/config`、`internal/health`、`internal/integration`、`internal/evaluation`、`internal/server`（除 import 与阶段 3 的门面替换外）

**完全不涉及**
- `config.yaml` / `config.ark.yaml` / `.env` / `Dockerfile` / `docker-compose*.yml` / `Makefile`（阶段 2 前）/ `internal/server/web/` / `scripts/`

---
---

# 第二部分　技能路由质量改造（P-SKILL-QUALITY）

> 目标：让模型在 13 个技能里选得准，并且让「选得准不准」可以被测量。
> 与第一部分的性质区别：P-REFACTOR 是「结构零行为变化」，本部分是「**改变路由质量**」（S1–S4 为零行为变化的文本修正，S5–S8 改变可观测性与指标）。

## 十、目标与非目标

### 10.1 目标

两件事：

1. **让模型选得对**——修掉「模型唯一用来匹配的那句话」与「同一技能块里的排除条件」自相矛盾的问题（S1–S4）。
2. **让选得对不对可测**——把「路由准确率」从一个感觉变成一个数字（S5–S7）。

### 10.2 问题定位（一句话）

模型每轮做技能匹配时，能看到的全部信息只有 4 行（`internal/skill/runtime.go:33-47`）：

```
- {目录名}: {description}
  适用场景：{scenarios}
  不适用于：{not_for}
  能力状态：{由 required_tools 与已接入工具推导}
```

其中 **`description` 是唯一的主匹配信号**——它和技能名写在同一行。而 13 个技能里 11 个的 `description` 仍是上游 Codex 英文原文，承诺本项目不具备的能力（`render_docx.py`、Poppler、`$CODEX_HOME/skills`、ChatGPT add-in），并且**与同一技能块里的 `not_for` 正面矛盾**。

模型在同一条目录项里同时读到「能生成 docx」和「不支持生成 Word 文件」——路由不稳是必然结果，不是模型不够聪明。**9B 本地模型在这种自相矛盾的信号下做 13 选 N，误选概率必然偏高。**

### 10.3 非目标

- **不改路由机制本身**：仍是「全量目录进提示词 + 模型调 `load_skills` 自选」，不引入第二个分类模型、不引入向量路由
- **不新增技能、不接入新工具**：S2 只声明工具依赖，不实现工具
- **不引入结构化路由协议**：默认路径仍是提示词约束；S8 的可校验输出只用于校验场景
- **不动 `skills.default` 与 `preferred` 的语义**：`cmd/console --skill` 与评测的强制指定能力保持不变

---

## 十一、现状核实

### 11.1 元数据消费者清单（改造前实测）

全项目只有 **6 个 frontmatter 键**出现过，各自的实际消费者如下：

| 键 | 出现 | 设计职责 | 本项目实际消费者 | 本部分处置 |
|---|---|---|---|---|
| `description` | 13/13 | 隐式路由的**主信号** | `runtime.go:33` | 保留（S3 改写 12 条） |
| `scenarios` | 13/13 | 「什么时候该选我」 | `runtime.go:34-36` | 保留 |
| `not_for` | 13/13 | 「什么时候别选我」 | `runtime.go:37-39` | 保留（S4 补 7 条分界） |
| `required_tools` | 1/13 | 能力边界：工具缺失即标注「不能承诺执行」 | `runtime.go:40-47` | 保留（S2 补齐 12 个） |
| `name` | 11/13 | 技能标识符 | **无** | **S1 删除** |
| `metadata.short-description` | 2/13 | 列表裁剪时的短描述 | **无** | **S1 删除** |

**规律：上游带进来的字段全是死的，本项目自己加的字段全是活的。**

### 11.2 为什么 `name` 是死配置（S1 的依据）

三步可验证：

1. `loader.go:59-64` 的 routing struct 只解析 `description` / `scenarios` / `not_for` / `required_tools`，**没有 `name`**。
2. yaml.v3 默认忽略未知键——全项目只有两处 `yaml.Unmarshal`（`config.go:380`、`loader.go:71`），**没有任何一处开 `KnownFields`**。所以解析不报错，`name` 被静默丢弃。
3. `Skill.Name` 全项目唯一写入点是 `loader.go:88`，值来自函数参数（即目录名）；4 个消费点（`runtime.go:33/64/65/89`）全读 `s.Name`。

所以 `presentations/SKILL.md` 里的 `name: Presentations` **从来没有进过提示词**，模型看到的一直是 `presentations`。

**上游来历**（`skills/` 下 11 个技能整份导入自 `github.com/openai/skills`，**只带了 `SKILL.md`**——`scripts/`、`references/`、`assets/`、`agents/openai.yaml` 均未导入。实测 `skills/**/*` 只有 13 个 `SKILL.md`）：

| 上游字段 | 上游用途 | 本项目状态 |
|---|---|---|
| `name`（必填，≤64） | 技能唯一标识符（slug）。支撑 `$skill-name` 显式调用，并按 name 去重 | ❌ 无消费者。本项目没有 `$name` 调用面，显式选择走 `skills.default` 配置、`preferred` 参数和 `load_skills` 的 `names`，三者都以目录名为准 |
| `description`（必填，≤1024） | 隐式路由的**唯一**触发面 | ✅ 生效（`runtime.go:33`） |
| `metadata.short-description` | 技能列表超上下文预算被裁剪时的短描述（上游上限约 2% 上下文 / 8000 字符，超了优先砍 description） | ❌ 无消费者。本项目目录无条件全量进提示词，没有这个预算机制 |
| `agents/openai.yaml` 的 `interface.display_name` | 面向人的展示名、图标、品牌色 | ❌ 未导入（文件不存在） |
| `scenarios` / `not_for` / `required_tools` | 上游**没有**这三个字段 | ✅ 生效（`loader.go:61-63`），是本项目自己加的 |

两条旁证：

- `name: Presentations` 连上游语义都不满足——上游的展示名一直放在 `agents/openai.yaml` 的 `interface.display_name` 里，`name` 始终是 slug 标识符。那个大写是**双重误解**。
- 被导入的正文里写着的 `render_docx.py`、「Use the helper scripts」、`.curated` 路径，指向的文件**在项目里并不存在**（导入时就没带 `scripts/`）。所以 S3 要改的不止 `description`，正文里的承诺同样落空。

**「展示名」需求现在由谁承担**：前端一行 JS——`internal/server/web/app.js:928` 的 `skill.replaceAll("_", " ")`。也就是说「给技能起个人看的名字」这个需求真实存在，只是实现位置在 `app.js`。将来若要中文友好名，应加 name→label 映射（或新增 frontmatter 键**并同时**改 `loader.go` 的 routing struct），**不是复活 `name`**。

### 11.3 真实接入的工具只有 5 个

`service.go:306-313` 硬编码 3 个 + 按开关注册 2 个：

| 工具名 | 来源 |
|---|---|
| `current_time` | 硬编码 |
| `write_note` | 硬编码（`internal/tool/note.go:59`，写文件需审批） |
| `load_skills` | 硬编码（`runtime.go:67`） |
| `local_file_read` | `local_files.enabled` 时注册（`internal/tool/local_file.go:64`） |
| `knowledge_search` | `rag.enabled` 时注册（`internal/tool/knowledge.go:30`） |

`required_tools` 的匹配是**精确字符串匹配**（`routing.go` 的 `missingTools`）。技能里写的工具名和实际注册名差一个字，就会被判成「缺少工具、不能承诺执行」。

### 11.4 可观测性缺口：路由结果目前看不见

| 位置 | 事实 |
|---|---|
| `config.yaml:40` | `execution_events.enabled: false` |
| `config.ark.yaml:55` | `execution_events.enabled: false` |
| `config.docker.yaml:35` | `execution_events.enabled: false` |
| `service.go`（execution 分支） | 关闭时直接 `return ctx, nil` → **`skill_loaded` / `skill_preloaded` 事件根本不发** |
| `internal/server/web/app.js:493` | 前端有对应渲染分支，**但收不到事件**（白写） |

**所以：改造前你无法在界面上看到模型这轮选了哪几个技能，只能靠读回答猜。**

⚠️ 关键区分：**`execution_events.enabled`（采集）与 `debug`（展示 + 技能名暴露 + 目录查询短路）是两个独立开关**，此前容易混为一谈：

| 配置 | `execution_events.enabled` | `debug` | 效果 |
|---|---|---|---|
| 当前三份配置 | `false` | `true`（yaml/ark）/ `false`（docker） | 本地能看到下拉框却**没有事件可选**；部署态**你也不留痕** |
| S5 之后（建议） | `true` | 本地 `true` / 部署 `false` | 部署态**内部留痕、用户面无感** |

### 11.5 改造规模（实测）

| 项 | 数量 |
|---|---|
| 需要改的 `SKILL.md` | **13** |
| S1 删除的行 | **15**（`name:` 11 行 + `metadata:` 与 `  short-description:` 共 4 行） |
| S2 补 `required_tools` | 12 个技能（`knowledge_qa` 已有） |
| S3 改写 `description` | 12 条（`knowledge_qa` 已达标） |
| S4 补 `not_for` 分界 | 7 个技能 |
| S5 / S6 改配置行 | 3 份配置文件（各 1–2 行） |
| S7 改 Go 文件 | 3（`evaluation.go`、`cmd/eval/main.go`、`eval_cases.json`） |
| S8 改 Go 文件 | 1（`skill/runtime.go`，可选） |
| **需要改的调用点（S1–S4）** | **0**（纯文本，不涉及任何 Go 代码） |

---

## 十二、逐项改造清单（S1–S8）

### S1　删除死配置（零行为变化）

**改法**：删掉 13 个 `SKILL.md` 里的所有 `name:` 与 `metadata:` 行（共 15 行）。

**当前不一致的写法**（正说明写的人以为它能控制展示）：

- `presentations/SKILL.md`: `name: Presentations`
- `spreadsheets/SKILL.md`: `name: "Spreadsheets"`
- `pdf`、`excel-live-control`: 带引号的 `name`

**为什么不「修好它」而是删除**：让 `name` 生效等于给「技能身份」开第二个来源，还得同步改 registry key（`runtime.go:83-86` 的校验以目录名为准）、`load_skills` 的参数校验、`validName` 正则（`loader.go:46`）与路径穿越防护。收益为负。

三个选项，只需选一个：

| 选项 | 成本 | 评价 |
|---|---|---|
| 删除 `name` / `metadata`（**本方案**） | 零 | 推荐。目录名是唯一事实来源 |
| 保留，但改成与目录名**完全一致** | 零 | 也站得住，当自注释文档用。前提是必须一致 |
| 维持现状（值不匹配、大小写各写各的） | 零 | 唯一的坏选项，因为它在主动误导后来者 |

**验收**：`grep -rn "^name:\|^metadata:" skills/` 无输出；`go test ./internal/skill/...` 全绿（元数据解析测试覆盖）。

---

### S2　补 `required_tools`（12 个技能）

**依据**：`runtime.go:40-47`。空声明时输出固定文案「未声明工具依赖，不能据此保证可执行」——**对模型没有任何行动指导**。声明之后才会输出真正有用的一句：「缺少工具 X，只可提供说明或替代方案，**不能承诺执行**」。

**建议映射**（未接入的工具名如实声明，这正是该字段的语义——不是造假，而是让模型知道哪些技能是空壳）：

| 技能 | `required_tools` | 接入状态 |
|---|---|---|
| `knowledge_qa` | `knowledge_search` | ✅ 已有，无需改 |
| `documents` | `local_file_read` | ✅ 已接入 |
| `report_writer` | `local_file_read`, `write_note` | ✅ 已接入（写笔记需审批） |
| `pdf` | `pdf_render`, `pdf_extract` | ❌ 未接入 → 判缺失 |
| `spreadsheets` | `workbook_read`, `workbook_write` | ❌ 未接入 |
| `presentations` | `deck_write` | ❌ 未接入 |
| `visualize` | `artifact_render` | ❌ 未接入 |
| `template-creator` | `template_write` | ❌ 未接入 |
| `skill-creator` | `skill_write` | ❌ 未接入 |
| `skill-installer` | `skill_install` | ❌ 未接入 |
| `computer-use` | `desktop_control` | ❌ 未接入 |
| `control-in-app-browser` | `browser_control` | ❌ 未接入 |
| `excel-live-control` | `excel_session` | ❌ 未接入 |

⚠️ **命名即契约**：右列 ❌ 那 9 个工具名尚未写进任何代码。一旦声明，将来真接入工具时**必须用同一个名字**，否则技能会永远被判「缺失」。建议现在一次定好，或先用统一前缀（如 `todo_pdf_render`）明确表示「尚未规划」。

**验收**：`documents` 的能力状态行从「未声明工具依赖」变为「声明的工具已接入」；`pdf` 变为「缺少工具 pdf_render, pdf_extract，不能承诺执行」。

---

### S3　改写 `description`（12 条）

**规则**：中文；句式 `做什么；不做什么`；不写工具名、脚本名、外部平台名；不承诺未接入能力。

| 技能 | 现状问题 | 建议 `description` |
|---|---|---|
| `documents` | 承诺 `render_docx.py` 生成 docx + 视觉 QA | 读取授权目录下的 UTF-8 文本与 DOCX 正文，做摘要、信息提取和内容组织；不生成、不编辑、不渲染文档文件 |
| `pdf` | 承诺 Poppler / reportlab / pdfplumber / pypdf | 讨论 PDF 的解析与生成方案，处理用户直接粘贴的文本；当前未接入 PDF 读写工具，不能读取或产出 PDF 文件 |
| `spreadsheets` | 承诺创建和校验 xlsx / Google Sheets | 分析用户提供的表格文本数据，设计表格结构与公式；当前未接入工作簿读写工具，不能打开或产出表格文件 |
| `presentations` | 承诺创建/编辑 PPT 与 Google Slides | 组织演示大纲与叙事结构，给出幻灯片内容建议；当前未接入演示文稿生成工具，不能产出幻灯片文件 |
| `excel-live-control` | 承诺通过 ChatGPT add-in 控制活动工作簿 | 说明活动 Excel 工作簿的操作方案；当前未接入 Excel 会话，不能操作已打开的工作簿 |
| `visualize` | 承诺直接产出可视化与交互工具 | 在对话中解释流程、比较方案、给出图表结构设计；不生成 HTML 页面，当前未接入可视化渲染工具 |
| `control-in-app-browser` | 承诺打开/导航/点击/输入/截图/本地测试 | 说明浏览器页面检查与交互的实现方案；当前未接入浏览器控制工具，不能访问或操作页面 |
| `computer-use` | 来自上游的同名能力声明 | 说明 Windows 桌面自动化的实现方案；当前未接入桌面控制工具，不能实际操作应用 |
| `skill-creator` | 承诺创建/更新 Codex skill | 设计技能规则草稿与元数据（description、scenarios、not_for、required_tools）；当前未接入技能目录写入工具，不能直接创建技能 |
| `skill-installer` | 承诺装入 `$CODEX_HOME/skills` | 说明技能安装流程与来源要求；当前未接入安装工具，不能安装或声明已安装技能 |
| `template-creator` | 承诺从多种来源创建模板技能 | 设计可复用的文档或表格模板内容与结构；当前未接入模板文件写入工具，不能落盘模板文件 |
| `report_writer` | 中文但未写清边界 | 基于已获得的事实撰写中文结构化报告，可写入笔记（需人工审批）；缺少事实依据时不编造内容 |
| `knowledge_qa` | 中文但未写清边界（**已基本达标，可不改**） | 检索私有知识库并给出带来源的回答；无检索依据时不推测私有事实 |

**验收**：逐条比对 frontmatter 与同技能块内 `not_for` 是否还矛盾；问「分析 Excel 并生成文件」，确认模型**不再承诺产出文件**，而是说明缺少工具。

---

### S4　`scenarios` / `not_for` 补互斥分界（7 个技能）

五个技能的场景短语目前彼此不互斥，模型没有可用的区分信号。把 `not_for` 改成**对比式**，直接点名该走谁：

| 技能 | 追加的 `not_for` 分界句 |
|---|---|
| `documents` | 纯 PDF 文件处理应走 pdf；表格数据分析应走 spreadsheets；演示稿应走 presentations |
| `pdf` | 用户粘贴的文本或 DOCX 正文不属于 pdf，应走 documents |
| `spreadsheets` | 活动 Excel 工作簿应走 excel-live-control；表格视觉设计应走 visualize |
| `presentations` | 文档排版应走 documents；图表本身应走 visualize |
| `template-creator` | 一次性产出内容不属于模板设计；应走 documents / spreadsheets / presentations |
| `visualize` | 技能推荐与普通问答不得生成可视化 |
| `computer-use` | 网页内的操作应走 control-in-app-browser |

---

### S5　打开执行事件采集（配置）

**依据**：见 11.4。三份配置当前全是 `false`。

**改法**：

| 文件 | `execution_events.enabled` | `debug` | 理由 |
|---|---|---|---|
| `config.yaml`（本地） | `false` → **`true`** | 保持 `true` | 本地要能看到 `skill_loaded`，否则 S7 之前无法验证路由 |
| `config.docker.yaml`（部署） | `false` → **`true`** | 保持 `false` | **内部留痕、用户面无感**：事件进 MySQL 可复盘，界面无技能入口 |
| `config.ark.yaml`（方舟联调） | `false` → **`true`** | 保持 `true` | 与本地同理 |

⚠️ **只动 `execution_events`，不要动 `debug`。** 这两件事已经解耦（`docs/SKILL-ROUTING.md` 有对照表），把它们混为一谈会导致「为了留痕而把技能名暴露给用户」这类错误。

**验收**：`GET /sessions/{id}/execution` 能返回本轮事件；界面（debug 为 true 时）出现「加载技能」条目。

---

### S6　放宽输出上限（配置）

多技能组合会让提示词侧膨胀（提示词不受 `max_completion_tokens` 限制），但**最终结论仍被输出上限卡住**——多技能交叉分析的答案会被截断。

**现状核对**：`config.ark.yaml:26` 已经是 `4096`，但 `config.yaml:9` 和 `config.docker.yaml:8` 仍是 `512`。

**改法**：

| 文件 | 现值 | 改为 | 理由 |
|---|---|---|---|
| `config.yaml:9` | `512` | `1024` | 本地 9B 推理慢（复杂问题约 3 分钟），不宜一次放太高 |
| `config.docker.yaml:8` | `512` | `2048` | 容器内视角同 ark，放宽更有价值 |
| `config.ark.yaml:26` | `4096` | 保持 | 已足够 |

---

### S7　评测增加「路由」维度 ⚠️ 依赖 P-REFACTOR 阶段 1

**现状**：`internal/evaluation/evaluation.go:17-22` 的 `Case` 只有 `question` / `expected` / `must_cite` / `should_refuse`——纯看答案文本，**技能选得对不对完全测不出来**。评测集只有 3 题（`internal/integration/eval_cases.json`），没有统计意义。

**埋点已经现成**：`runtime.go:94-97` 已写入 `skill_names` / `requested_names`。

**做法**：

1. `Case` 增加 `ExpectSkills []string \`json:"expect_skills,omitempty"\``
2. 评测执行时用 `execution.NewRecorder(0)` 挂到 `ChatWithSink` 上
3. 跑完扫 `Events()` 里 `SkillLoaded` 的 `payload.skill_names`，做集合断言
4. `Report` 增加 `RoutingAccuracy float64`
5. 评测集从 3 题扩到 **≥20 题**，覆盖 S4 里那几组易混场景

做完这一步，`make eval` 才能吐出「路由准确率」这个数字，而不是靠感觉。

⚠️ **顺序约束**：本步改的 3 个文件中有 2 个（`internal/evaluation/evaluation.go`、`cmd/eval/main.go`）也在 P-REFACTOR 阶段 1 的 import 改写清单里。**先做 P-REFACTOR 阶段 1 再写 S7**，这样新增 import 直接写最终路径，无需二次改写。

---

### S8　提示词加「可校验输出」（可选，成本高）

要求模型在回答前先输出选定技能名列表，把「模型遵循」变成可断言的文本。

**只在校验阶段用**，生产提示词不加（额外吃 token，而 512 上限下更紧张；S6 之后略缓解）。

**依赖**：与 S7 同样必须在 P-REFACTOR 阶段 1 之后——它改的 `internal/skill/runtime.go` 正是阶段 3.2（工具构造下沉）的目标文件。

---

## 十三、必须处理的坑

| # | 坑 | 后果 | 应对 |
|---|---|---|---|
| 1 | **`description` 与同块 `not_for` 自相矛盾** | 模型多选或全选，路由不稳的**根本原因** | S2/S3/S4 必须同批完成，不要只改一半 |
| 2 | **未接入工具名一旦写进技能即成契约** | 将来接入时必须同名，否则技能永远判「缺失」 | 先定名，或统一用 `todo_` 前缀 |
| 3 | **空 `required_tools` 与「声明了但缺失」语义完全不同** | 为了「好看」给所有技能都填上会导致能力状态全部是缺失 | 如实声明：已接入的写真实名，未接入的写规划名 |
| 4 | **把事件开关与展示开关混为一谈** | 以为部署态留痕了，实际什么都没存 | S5 只动 `execution_events`，**不动 `debug`** |
| 5 | **S7 / S8 在 P-REFACTOR 阶段 1 之前做** | 写进去的 import 被二次改写；且 P-REFACTOR 的行号基准失效 | 严格按第十七节的顺序 |
| 6 | **本地 `max_completion_tokens` 一次调到 4096** | 本地 9B 单请求可占用数分钟，并发只有 2，直接拖垮本机 | 本地封顶 `1024`；要长输出就把 `config.yaml` 切到方舟配置 |
| 7 | **给 `name` 开第二套语义** | 「技能身份」出现两个来源，`load_skills` 校验与路径穿越防护要跟着改 | S1 直接删除，目录名是唯一事实来源 |
| 8 | **改完 `SKILL.md` 忘了模型侧缓存** | 每轮扫描 `skills/` 会重新读取（`runtime.go:21`），无需重启服务 | 无需处理，但要知道「改完立即生效」意味着**改错了也立即生效** |

---

## 十四、实施顺序

| 步骤 | 内容 | 涉及文件 | 性质 | 可独立提交 |
|---|---|---|---|---|
| S1 | 删除 `name` / `metadata` | 13 个 `SKILL.md` | 纯文本 | ✅ |
| S2 | 补 `required_tools` | 12 个 `SKILL.md` | 纯文本 | ✅ |
| S3 | 改写 `description` | 12 个 `SKILL.md` | 纯文本 | ✅ |
| S4 | 补 `not_for` 分界 | 7 个 `SKILL.md` | 纯文本 | ✅ |
| S5 | 打开采集 | 3 份配置文件 | 配置 | ✅ |
| S6 | 放宽 token 上限 | 2 份配置文件 | 配置 | ✅ |
| S7 | 评测路由维度 | 3 个文件 | 代码 | ✅ |
| S8 | 可校验输出 | 1 个文件（可选） | 代码 | ✅ |

**理由**：先清噪声（S1）→ 再给模型能力状态（S2）→ 再改主信号（S3）→ 再补边界（S4）。

S1–S6 是**一小时内能完成的量**，且立刻影响路由质量；S7 是唯一有实质工作量的部分。

**S1–S6 与 AUTH-PLAN、P-REFACTOR 零文件交集**（除 S5/S6 与 AUTH-PLAN 同改 `config.yaml`，但段落不同），因此**可以在任何时刻插空做**——建议放在最前面，因为成本最低而收益立即。

**S7 / S8 必须排在 P-REFACTOR 阶段 1 之后。**

---

## 十五、待决策事项

| # | 决策 | 建议 | 理由 |
|---|---|---|---|
| 1 | S2 里 9 个未接入工具名怎么定 | **按方案给定，或统一 `todo_` 前缀** | 命名即契约，改名的代价随时间上升 |
| 2 | `config.docker.yaml` 是否开 `execution_events` | **开**（`debug` 保持 `false`） | 否则上线后无法复盘；且开了也不向用户暴露 |
| 3 | 评测集扩到多少题 | **≥20 题** | 3 题算不出有意义的准确率 |
| 4 | S8 是否做 | **暂不做** | 校验价值有限，且与 512 token 上限冲突。S6 之后再评估 |
| 5 | 是否给「中文友好展示名」加配置 | **不加** | 现由 `app.js:928` 的 `replaceAll` 承担，够用。真要做就加新键并同时改 loader，别复活 `name` |
| 6 | 是否给所有技能都补 `required_tools` | **是，但如实声明** | 见坑 3 |

---

## 十六、验收清单

### 自动检查
- [ ] `grep -rn "^name:\|^metadata:" skills/` 无输出（S1）
- [ ] `go test ./internal/skill/...` 全绿（元数据解析、目录不泄露正文）
- [ ] `go test ./internal/server/... -run "TestSkillExposureIsDebugOnly|TestWebSocketReady"` 通过（暴露面门禁未被破坏）
- [ ] `make check` 全绿
- [ ] `make eval` 输出含 `routing_accuracy`（S7）

### 人工断言（`debug: true` 下）
- [ ] 问「目前都有什么技能」→ 逐条核对每个技能的「能力状态」与 S2 表格一致
- [ ] 问「分析这个 Excel 并生成文件」→ 模型**不承诺产出文件**，说明缺少工具
- [ ] 问「我要总结 Word 工单，推荐什么技能」→ 只说 documents，不牵扯 pdf / spreadsheets
- [ ] 界面执行面板出现「加载技能：…」条目（S5 生效）
- [ ] 切换 `debug: false` 重启 → `GET /skills` 404、界面无技能入口、但 `GET /sessions/{id}/execution` 仍有事件（**验证两个开关确实解耦**）

---
---

# 第三部分　三份方案的关系与统一实施顺序

## 十七、三方统一实施顺序

```
阶段 0  git init + 基线提交            ← AUTH-PLAN 与 P-REFACTOR 共用同一次
   ↓
S1–S6   技能质量（纯文本 + 配置）        ← 与另两份零文件交集，随时可做，建议最先
   ↓
AUTH-PLAN 阶段 1–5                     ← 上线阻塞项，必须在 P-REFACTOR 之前
   ↓
P-REFACTOR 阶段 1–2（机械平移 + 边界守卫）
   ↓
S7–S8   评测路由维度 + 可校验输出        ← 必须夹在阶段 1 与阶段 3.2 之间
   ↓
P-REFACTOR 阶段 3–4（构造下沉 + 门面）    ← 可选增强；3.2 也改 skill/runtime.go，须排在 S8 之后
```

**三条排序理由**：

| 顺序约束 | 理由 |
|---|---|
| S1–S6 放最前 | 零文件交集（仅 S5/S6 与 AUTH 同改 `config.yaml` 的不同段落），纯文本改动，改完立即生效，是最低成本的质量提升 |
| AUTH-PLAN 在 P-REFACTOR 之前 | AUTH-PLAN 是**上线阻塞项**；且它全文带行号跳转，P-REFACTOR 会重写 29 行 import + 移动 37 个文件，先做会让 AUTH-PLAN 的行号基准全部失效（两份文档对此已有一致结论） |
| S7–S8 夹在 P-REFACTOR 阶段 1 与阶段 3.2 之间 | **上界**：S7 改的 `evaluation.go` / `cmd/eval/main.go` 在阶段 1 的 import 清单里，S8 改的 `skill/runtime.go` 在阶段 3.2 的改造清单里——先做会被二次改写。**下界**：阶段 3.2 的目标文件正是 `skill/runtime.go`，若赶在 S8 之前做，反过来 S8 要二次改同一个文件 |

**阶段 0 只需一次**：`git init` + 基线提交，三份方案共用。

```bash
cd E:/11/my-eino-app
git init
# 确认 .gitignore 覆盖：data/、.env、eino-server、会话 JSON、*.checkpoint、*.approval.json
git add -A && git commit -m "baseline before refactor / auth / skill-quality"
go build ./... && go vet ./... && go test -p 1 ./...   # 记录基线通过数
```

### 顺序之外的两条边界

| 事项 | 说明 |
|---|---|
| **S5 早于 AUTH-PLAN 会留下无归属的执行记录** | AUTH-PLAN 的 H（Execution 隔离）才给执行记录加归属校验。S5 打开采集后、H 落地前产生的记录没有 owner，AUTH-PLAN 上线后这批记录不可归属（丢弃或回填）。本地单人自用无影响；**对外上线前**建议把 S5 排到 AUTH-PLAN 阶段 5 之后，或明确「旧记录可丢弃」 |
| **阶段 0 只做一次，commit message 以本节为准** | 第六节与 `AUTH-PLAN.md` 第六节各自写过一版文案（`baseline before eino convergence` / `baseline before multiuser auth`）。三份方案共用同一次 `git init`，只需提交一次；不要按那两节再执行一遍 |

### 开工门槛：按「现在能否直接动手」分类

上一节的顺序回答「先做哪个」，这一节回答「哪个现在就能做」。两处待决策表（第七节 5 条 + 第十五节 6 条，共 11 项）中真正会卡住开工的只有 **2 项**：

| 类别 | 步骤 | 前置条件 |
|---|---|---|
| **现在就能做，无决策依赖** | 阶段 0（`git init` + 基线）、阶段 1（机械平移）、S1（删死键）、S5（打开采集）、S6（放宽 token 上限） | 无（阶段 1 需在阶段 0 之后） |
| **需先拍板一个参数** | S2 → S3 → S4 | **先定 9 个未接入工具名**（第十五节决策 1）。三者必须同批完成（坑 1），且命名即契约，改名代价随时间上升 |
| **需先完成阶段 1** | S7（评测路由维度） | 阶段 1 完成；另建议评测集同步扩到 ≥20 题（决策 3） |
| **建议跳过** | S8（可校验输出）、阶段 4（切反向依赖） | 决策 4 / 第七节决策 5 |
| **本方案不含，需另开** | AUTH-PLAN 阶段 1–5、第十九节三条缺口 | AUTH-PLAN 为独立文档 |

**唯一「不做就没有退路」的一步是阶段 0**：当前项目没有 git，而后续任何一步都可批量改写——阶段 1 平移 37 个文件、S1 一次改 13 个 `SKILL.md`。没有基线提交，这些操作都不可回退。

**S1 的执行口径**：按第十一节 11.2 的证据链，`name` / `metadata` 均为无消费者的死键，**直接删除**；不采用「保留但改成与目录名一致」的折中（该选项仅在需要自注释时才成立，且必须逐字一致）。

---

## 十八、文件级交集分析（三方案）

| 文件 | AUTH-PLAN | P-REFACTOR | P-SKILL-QUALITY |
|---|---|---|---|
| `skills/*/SKILL.md`（13） | — | — | **S1–S4** |
| `config.yaml` | 新增 `auth:` 段 | — | **S5**（`execution_events`）、**S6**（`max_completion_tokens`） |
| `config.docker.yaml` | — | — | **S5**、**S6** |
| `config.ark.yaml` | — | — | **S5**（`execution_events`）。**不含 S6**——它已是 `4096` |
| `internal/evaluation/evaluation.go` | — | 1 行 import（阶段 1） | **S7**（加 `Case` 字段） |
| `cmd/eval/main.go` | — | 3 行 import（阶段 1） | **S7**（挂 Recorder + 指标） |
| `internal/integration/eval_cases.json` | — | — | **S7**（扩题） |
| `internal/skill/runtime.go` | — | 阶段 3.2（工具构造下沉） | **S8**（校验模式，可选） |
| `internal/server/service.go` | owner 流转、9 处调用点 | 4 行 import | — |
| `cmd/server/main.go` | 装配 auth | 1 行 import | — |
| `internal/session/*`、`memory/*`、`checkpoint/*`、`config/*` | 主要战场 | 不动 | — |
| `internal/server/web/*` | 登出按钮 + 401 处理 | 不动 | — |

**结论**：

- **S1–S4 与另两份方案零交集**（只碰 `skills/` 下的 md）。
- **S5/S6 与 AUTH-PLAN 同改 `config.yaml`**，但段落不重叠（`execution_events:` / `openai.max_completion_tokens` vs 新增 `auth:`），并行编辑不会冲突，只需避免同时提交同一文件。
- **S7/S8 是唯一存在真实顺序依赖的项**，约束已在第十七节明确。
- **AUTH-PLAN ∩ P-REFACTOR 的交集是 2 个文件**（与第八节 8.2、`AUTH-PLAN.md` 第七节一致）：`internal/server/service.go`（import 4 行 + owner 流转、9 处调用点）与 `cmd/server/main.go`（import 1 行 + 装配 auth 模块）。**S1–S8 完全不碰这两个文件。**

---

## 十九、尚未归属任何方案的上线缺口

合并两轮核查后，以下三条**既不属 AUTH-PLAN、也不属本方案**，但都属上线前应处理项。列在这里是为了避免它们继续悬空：

| # | 缺口 | 位置证据 | 性质 |
|---|---|---|---|
| 1 | **无限流与无配额** | 只有全局 `runtime.max_concurrency`（本地/部署 2，ark 8）+ 同 session 排队，无 per-IP / per-user 限流，无 token 预算 | `config.ark.yaml` 把并发提到 8 意味着**未授权请求能同时打满 8 路计费调用**——并发越高，缺限流的代价越贵 |
| 2 | **备份未形成流程** | `data/backups/` 下只有 2026-09-10 的**一次手工**全库 dump，无定时任务、无恢复演练、无保留策略；Milvus 三个数据卷无快照流程 | 只备份不恢复等于没备份 |
| 3 | **容器停机不优雅（一行修复）** | `cmd/server/main.go:24` 是 `signal.NotifyContext(context.Background(), os.Interrupt)`——**只捕获 SIGINT**，而 `docker stop` 默认发 **SIGTERM**。走不到 `server.Shutdown`，10 秒优雅期与 `defer service.Close()` 全部失效 | 正在跑的请求被硬切、执行事件可能丢终态 |

**建议**：第 3 条成本极低（一行），可并入任意一次提交；第 1、2 条建议在 AUTH-PLAN 完成后单独立项。

---

## 二十、合并后的完整受影响文件清单

### P-SKILL-QUALITY（第二部分）

**S1–S4 修改（13，纯文本）**
- `skills/` 下全部 13 个 `SKILL.md`

**S5 修改（3，配置）**
- `config.yaml`、`config.docker.yaml`、`config.ark.yaml`（三份的 `execution_events.enabled` 均为 `false`）

**S6 修改（2，配置）**
- `config.yaml`、`config.docker.yaml`（`512 → 1024`）。`config.ark.yaml` 已是 `4096`，**不需要改**

**S7 修改（3）**
- `internal/integration/eval_cases.json`（评测集由 3 题扩到 ≥20 题；**文件已存在**，非新增）
- `internal/evaluation/evaluation.go`
- `cmd/eval/main.go`

**S8 修改（1，可选）**
- `internal/skill/runtime.go`

**完全不涉及**
- `internal/skill/loader.go`（S1 是删技能文件里的行，**不改解析代码**）
- `internal/server/`（技能暴露面门禁已在前一轮完成）
- `internal/server/web/`、`Dockerfile`、`docker-compose*.yml`、`scripts/`

### P-REFACTOR（第一部分）

见第一部分末尾「附：P-REFACTOR 受影响文件清单」。

### 两者合并后的重叠

| 文件 | P-REFACTOR 侧 | P-SKILL-QUALITY 侧 | 顺序约束 |
|---|---|---|---|
| `internal/evaluation/evaluation.go` | 阶段 1：1 行 import | S7：加 `Case.ExpectSkills` | S7 在阶段 1 之后 |
| `cmd/eval/main.go` | 阶段 1：3 行 import | S7：挂 Recorder + 指标 | S7 在阶段 1 之后 |
| `internal/skill/runtime.go` | 阶段 3.2：工具构造下沉 | S8：校验模式（**可选**，决策 4 建议暂不做） | S8 夹在阶段 1 与阶段 3.2 之间 |

**计数口径**：若做 S8，重叠是 **3 个文件**；按决策 4 跳过 S8，则是 **2 个文件**（前两行）。此处此前写「只有 2 个」漏算了 `internal/skill/runtime.go`——该文件在第十八节表格里已明确列出双方都改，现两处口径统一。
