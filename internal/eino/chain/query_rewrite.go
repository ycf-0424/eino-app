package chain

import "strings"

// RewriteQuery 是查询改写节点的轻量实现：先清理空白和常见问句噪声。
// 后续如果需要更复杂的改写，可在这里替换为一次 Ollama ChatModel 调用，
// 而不会影响后面的检索、Prompt 和结构化输出节点。
func RewriteQuery(query string) string {
	query = strings.Join(strings.Fields(strings.TrimSpace(query)), " ")
	query = strings.TrimRight(query, "？?!！。")
	return query
}
