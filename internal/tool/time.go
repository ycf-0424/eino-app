// Package tool 提供 Agent 可以调用的业务工具。
package tool

import (
	"context"
	"fmt"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

// TimeInput 是模型调用 current_time 时生成的 JSON 参数结构。
type TimeInput struct {
	// Timezone 使用 IANA 时区名称；为空时默认使用 Asia/Shanghai。
	Timezone string `json:"timezone"`
}

// NewTimeTool 返回一个只读时间工具。timezone 支持 Asia/Shanghai、UTC 等 IANA 名称。
func NewTimeTool() einotool.InvokableTool {
	info := &schema.ToolInfo{
		Name: "current_time",
		Desc: "Get the current date and time. Use this whenever the user asks for the current time or date.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"timezone": {Desc: "IANA timezone, for example Asia/Shanghai or UTC", Type: schema.String},
		}),
	}
	return utils.NewTool(info, currentTime)
}

func currentTime(_ context.Context, input TimeInput) (string, error) {
	zone := input.Timezone
	if zone == "" {
		// 项目默认面向中国用户，因此使用上海时区作为默认值。
		zone = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return "", fmt.Errorf("invalid timezone %q: %w", zone, err)
	}
	return time.Now().In(loc).Format("2006-01-02 15:04:05 MST"), nil
}
