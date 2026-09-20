package memory

import "testing"

func TestMemoryQueryMatchesOnlyRelevantPendingFact(t *testing.T) {
	fact := Fact{Key: "chat_model", Value: "项目决定使用 Ollama 运行 qwen3.5:9b 作为聊天模型。"}
	if !memoryQueryMatches("项目资料中记录的聊天模型使用什么？", fact) {
		t.Fatal("related Chinese query should match pending project fact")
	}
	if memoryQueryMatches("项目资料中火星办公室地址是什么？", fact) {
		t.Fatal("unrelated query must not match a pending project fact")
	}
}

func TestMemoryQueryMatchesEnglishIdentifier(t *testing.T) {
	fact := Fact{Key: "vector_database", Value: "Milvus"}
	if !memoryQueryMatches("what vector database is used", fact) {
		t.Fatal("related English query should match pending project fact")
	}
	if memoryQueryMatches("who is the finance officer", fact) {
		t.Fatal("unrelated English query must not match pending project fact")
	}
}
