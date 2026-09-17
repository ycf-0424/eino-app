// Package eino 是业务包与 eino 框架之间的唯一接触面。
//
// 为什么需要它（EXECUTION-PLAN 步骤 3.3.3）：把 7 个包收进 internal/eino/ 只解决了
// 「编排代码放在哪」，业务包仍在直接 import cloudwego/eino/components/*。这些类型别名
// 让业务包改从本包引用同一批类型 —— 零运行时开销、不产生包装层，但对框架的依赖收敛到
// 一个文件，将来升级或替换框架时只改这里，业务包不用动。
//
// schema 不在此列：消息与工具参数的结构体是跨包数据契约，业务包继续直接用它是刻意的。
package eino

import (
	"github.com/cloudwego/eino/components/embedding"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
)

// ChatModel 是需要工具调用能力的对话模型。
type ChatModel = einomodel.ToolCallingChatModel

// BaseChatModel 只需要 Generate / Stream。用于抽取、改写这类不涉及工具调用的场景，
// 契约比 ChatModel 更宽（ToolCallingChatModel 是它的超集）。
type BaseChatModel = einomodel.BaseChatModel

// BaseTool 是所有工具的基础接口。
type BaseTool = einotool.BaseTool

// InvokableTool 是可以被模型按名调用并返回文本结果的工具。
type InvokableTool = einotool.InvokableTool

// Embedder 把文本转成向量。
type Embedder = embedding.Embedder
