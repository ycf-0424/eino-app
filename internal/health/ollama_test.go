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
