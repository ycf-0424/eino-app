package server

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"my-eino-app/internal/config"
)

// readyBody 是 /health/ready 返回体的测试视图。
type readyBody struct {
	Data struct {
		Status string `json:"status"`
		Checks []struct {
			Name      string `json:"name"`
			OK        bool   `json:"ok"`
			Error     string `json:"error,omitempty"`
			LatencyMS int64  `json:"latency_ms"`
		} `json:"checks"`
	} `json:"data"`
}

func callReady(t *testing.T, service *Service) (int, readyBody) {
	t.Helper()
	rec := httptest.NewRecorder()
	service.handleReady(rec, httptest.NewRequest("GET", "/health/ready", nil))
	var body readyBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析返回体失败: %v (body=%s)", err, rec.Body.String())
	}
	return rec.Code, body
}

// 纯本地文件模式（无 MySQL、无 RAG）时没有可探的依赖，ready 退化为 liveness：
// 返回 200 与空列表，而不是硬编码一个假的健康项。
func TestReadyWithoutDependencies(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	code, body := callReady(t, service)
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if body.Data.Status != "ok" {
		t.Fatalf("status = %q", body.Data.Status)
	}
	if len(body.Data.Checks) != 0 {
		t.Fatalf("不应有探测项，实际 %+v", body.Data.Checks)
	}
}

// cfg 为 nil（未初始化的 Service）时不能 panic：ready 会被编排在启动早期调用。
func TestReadyWithNilConfig(t *testing.T) {
	service := &Service{}
	code, body := callReady(t, service)
	if code != http.StatusOK || len(body.Data.Checks) != 0 {
		t.Fatalf("code = %d, checks = %+v", code, body.Data.Checks)
	}
}

// Milvus 不可达时必须返回 503 并指出是哪一项失败，而不是笼统的「不健康」。
func TestReadyReportsMilvusFailure(t *testing.T) {
	// 占一个端口再立刻释放：拿到一个几乎必然无人监听的地址。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	listener.Close()

	service := &Service{cfg: &config.Config{
		RAG: config.RAG{Enabled: true, Store: "milvus", Milvus: config.Milvus{Address: address}},
	}}
	code, body := callReady(t, service)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", code)
	}
	if body.Data.Status != "unavailable" {
		t.Fatalf("status = %q", body.Data.Status)
	}
	found := false
	for _, item := range body.Data.Checks {
		if item.Name == "milvus" {
			found = true
			if item.OK || item.Error == "" {
				t.Fatalf("milvus 项应失败并带错误信息: %+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("返回体缺少 milvus 项: %+v", body.Data.Checks)
	}
}

// session.store=file 时不探测 MySQL：没连它就不该因为它的状态被判不健康。
func TestReadySkipsMySQLForFileStore(t *testing.T) {
	service := &Service{cfg: &config.Config{Session: config.Session{Store: "file"}}}
	_, body := callReady(t, service)
	for _, item := range body.Data.Checks {
		if item.Name == "mysql" {
			t.Fatal("file 后端不应探测 MySQL")
		}
	}
}

// 自动记忆始终使用 Milvus 索引；即使文档 RAG 改成 redis，ready 也不能漏报。
func TestReadyChecksMilvusWhenMemoryEnabled(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	listener.Close()

	service := &Service{cfg: &config.Config{
		RAG:    config.RAG{Enabled: true, Store: "redis", Milvus: config.Milvus{Address: address}},
		Memory: config.Memory{Enabled: true},
	}}
	code, body := callReady(t, service)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", code)
	}
	for _, item := range body.Data.Checks {
		if item.Name == "milvus" {
			if item.OK || item.Error == "" {
				t.Fatalf("milvus 项应失败并带错误信息: %+v", item)
			}
			return
		}
	}
	t.Fatalf("memory.enabled 时返回体缺少 milvus 项: %+v", body.Data.Checks)
}
