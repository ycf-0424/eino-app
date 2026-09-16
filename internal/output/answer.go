// Package output 定义跨 Agent、Chain 和未来 Web API 共用的结构化回答。
package output

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// Answer 是 RAG 对外返回的稳定数据结构。
type Answer struct {
	Answer     string   `json:"answer"`
	Sources    []string `json:"sources,omitempty"`
	Confidence float64  `json:"confidence"`
	Error      string   `json:"error,omitempty"`
}

// ParseModelAnswer 解析模型 JSON；模型偶尔返回 Markdown 围栏时也能兼容。
// 解析失败时保留原文，避免因为格式问题丢失模型回答。
func ParseModelAnswer(message *schema.Message, sources []string, confidence float64) Answer {
	result := Answer{Sources: unique(sources), Confidence: confidence}
	if message == nil {
		result.Error = "model returned an empty response"
		return result
	}
	text := strings.TrimSpace(message.Content)
	text = regexp.MustCompile("(?s)^```(?:json)?\\s*(.*?)\\s*```$").ReplaceAllString(text, "$1")
	var parsed struct {
		Answer     string  `json:"answer"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(text), &parsed); err == nil && parsed.Answer != "" {
		result.Answer = parsed.Answer
		if parsed.Confidence > 0 {
			result.Confidence = parsed.Confidence
		}
		return result
	}
	result.Answer = text
	result.Error = "model response was not valid JSON; returned as plain text"
	return result
}

// JSON 将结构化回答编码为便于 API 和脚本消费的 JSON。
func (a Answer) JSON() ([]byte, error) { return json.MarshalIndent(a, "", "  ") }

func unique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
