package tool

import (
	"context"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

// NameListFunc 是「按名称列表读取内容」类工具的业务函数签名。
type NameListFunc func(ctx context.Context, names []string) (string, error)

// NewNameListTool 把一个接收名称列表的业务函数包装成可调用工具
// （EXECUTION-PLAN 步骤 3.3.2 所称的「工具构造下沉」）。
//
// 为什么放在这里：参数 schema 的形状、入参解组、工具接口的实现细节都是 eino 框架的
// 知识。业务包（internal/skill）只为「把函数注册成工具」就该 import
// components/tool/utils，是把框架细节漏进了业务层。下沉后业务侧只提供纯函数，
// 参数声明由本层的调用方传入，包装留在这一处。
func NewNameListTool(name, desc, paramDesc string, fn NameListFunc) einotool.InvokableTool {
	info := &schema.ToolInfo{
		Name: name,
		Desc: desc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"names": {
				Type:     schema.Array,
				ElemInfo: &schema.ParameterInfo{Type: schema.String},
				Required: true,
				Desc:     paramDesc,
			},
		}),
	}
	return utils.NewTool(info, func(ctx context.Context, input struct {
		Names []string `json:"names"`
	}) (string, error) {
		return fn(ctx, input.Names)
	})
}
