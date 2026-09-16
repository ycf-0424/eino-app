// Package health 提供本地 Ollama 运行状态和模型可用性检查。
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type tagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}
type embeddingsResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

// CheckOllama 检查服务、聊天模型和 Embedding 模型；失败时返回可操作的错误信息。
func CheckOllama(ctx context.Context, baseURL, chatModel, embeddingModel string, dimension int, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	base := strings.TrimRight(strings.TrimSuffix(baseURL, "/v1"), "/")
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("ollama health request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ollama is unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ollama health returned HTTP %d", resp.StatusCode)
	}
	var tags tagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return fmt.Errorf("decode ollama tags: %w", err)
	}
	if !hasModel(tags.Models, chatModel) {
		return fmt.Errorf("ollama chat model %q is not installed", chatModel)
	}
	if !hasModel(tags.Models, embeddingModel) {
		return fmt.Errorf("ollama embedding model %q is not installed", embeddingModel)
	}
	body := fmt.Sprintf(`{"model":%q,"input":"health check"}`, embeddingModel)
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/embeddings", strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		return fmt.Errorf("ollama embedding request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ollama embedding returned HTTP %d", resp.StatusCode)
	}
	var embedding embeddingsResponse
	if err := json.NewDecoder(resp.Body).Decode(&embedding); err != nil {
		return fmt.Errorf("decode embedding response: %w", err)
	}
	if len(embedding.Data) != 1 || len(embedding.Data[0].Embedding) != dimension {
		got := 0
		if len(embedding.Data) > 0 {
			got = len(embedding.Data[0].Embedding)
		}
		return fmt.Errorf("embedding dimension mismatch: got %d, want %d", got, dimension)
	}
	return nil
}

func hasModel(models []struct {
	Name string `json:"name"`
}, wanted string) bool {
	wanted = strings.TrimSpace(wanted)
	for _, model := range models {
		if model.Name == wanted || strings.TrimSuffix(model.Name, ":latest") == strings.TrimSuffix(wanted, ":latest") {
			return true
		}
	}
	return false
}
