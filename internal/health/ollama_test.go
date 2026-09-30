package health

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheckOllama(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"models":[{"name":"qwen3.5:9b"},{"name":"bge-m3:latest"}]}`)
	})
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"data":[{"embedding":[1,2,3]}]}`) })
	server := httptest.NewServer(mux)
	defer server.Close()
	if err := CheckOllama(context.Background(), server.URL+"/v1", "qwen3.5:9b", "bge-m3", 3, time.Second); err != nil {
		t.Fatal(err)
	}
}
func TestCheckOllamaMissingModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"models":[]}`) }))
	defer server.Close()
	if err := CheckOllama(context.Background(), server.URL+"/v1", "missing", "bge-m3", 3, time.Second); err == nil {
		t.Fatal("expected missing model error")
	}
}

func TestIsOllamaBaseURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "local v1", url: "http://localhost:11434/v1", want: true},
		{name: "docker host", url: "http://host.docker.internal:11434/v1", want: true},
		{name: "local other port", url: "http://127.0.0.1:18181/v1", want: false},
		{name: "remote provider", url: "https://ark.cn-beijing.volces.com/api/v3", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsOllamaBaseURL(test.url); got != test.want {
				t.Fatalf("IsOllamaBaseURL(%q) = %v, want %v", test.url, got, test.want)
			}
		})
	}
}

func TestShouldCheckOllamaRequiresBothLocalEndpoints(t *testing.T) {
	if !ShouldCheckOllama("http://localhost:11434/v1", "http://localhost:11434/v1") {
		t.Fatal("expected local chat and embedding endpoints to enable Ollama check")
	}
	if ShouldCheckOllama("http://localhost:11434/v1", "https://ark.cn-beijing.volces.com/api/v3") {
		t.Fatal("did not expect mixed local and remote endpoints to enable Ollama check")
	}
}
