//go:build mysql_integration

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"my-eino-app/internal/config"
	"my-eino-app/internal/execution"
	"my-eino-app/internal/session"
)

type chatEnvelope struct {
	Data struct {
		Answer string            `json:"answer"`
		RunID  string            `json:"run_id"`
		Events []execution.Event `json:"events"`
	} `json:"data"`
	Error string `json:"error"`
}

type executionEnvelope struct {
	Data struct {
		Runs   []execution.Run   `json:"runs"`
		Events []execution.Event `json:"events"`
	} `json:"data"`
	Error string `json:"error"`
}

// TestChatRecordsExecutionEvents 在真实 MySQL 上验证 P8 端到端链路：
// POST /chat 创建 run 并写入事件，同步响应内联事件数组，
// 查询接口可按 run 回放，删除会话后无孤儿记录。
func TestChatRecordsExecutionEvents(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"执行记录验证\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer mock.Close()

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Session.Store = "mysql"
	cfg.Session.MaxMessages = 20
	cfg.RAG.Enabled = false
	cfg.Memory.Enabled = false
	cfg.LocalFiles.Enabled = false
	cfg.Agent.MultiAgent = false
	cfg.Debug = false
	cfg.Agent.CheckpointDir = t.TempDir()
	cfg.Skills.Dir = t.TempDir()
	cfg.Skills.Default = ""
	cfg.OpenAI.BaseURL = mock.URL + "/v1"
	cfg.OpenAI.APIKey = "test"
	cfg.Retry.MaxAttempts = 1
	probe, err := session.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	if _, err = probe.DB().ExecContext(context.Background(), "SELECT 1 FROM execution_runs LIMIT 0"); err != nil {
		t.Skipf("execution schema is not installed: %v", err)
	}
	// 开启 P8 开关；Load 阶段默认关闭，因此手动补齐运行参数。
	cfg.ExecutionEvents.Enabled = true
	cfg.ExecutionEvents.QueueSize = 64
	cfg.ExecutionEvents.MaxEventsPerRun = 500
	cfg.ExecutionEvents.RetentionDays = 0

	service, err := NewService(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer service.sessions.Close()
	if service.Executions() == nil {
		t.Fatal("开启开关后执行记录后端为空")
	}
	handler := service.Handler()
	id := "exec-e2e-" + uuid.NewString()
	if err = service.sessions.Create("", id); err != nil {
		t.Fatalf("签发测试会话: %v", err)
	}
	defer service.sessions.Delete("", id)
	defer service.DeleteExecutions(context.Background(), id)

	chatBody, _ := json.Marshal(map[string]string{"session_id": id, "query": "请回答"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/chat", bytes.NewReader(chatBody)))
	if rec.Code != 200 {
		t.Fatalf("POST /chat 状态=%d 响应=%s", rec.Code, rec.Body.String())
	}
	var chat chatEnvelope
	if err = json.Unmarshal(rec.Body.Bytes(), &chat); err != nil {
		t.Fatal(err)
	}
	if chat.Data.RunID == "" {
		t.Fatal("同步响应缺少 run_id")
	}
	if len(chat.Data.Events) == 0 {
		t.Fatal("同步响应未内联事件数组")
	}
	for _, ev := range chat.Data.Events {
		if ev.Type == execution.Chunk {
			t.Fatal("同步接口事件数组不应包含 chunk")
		}
	}

	// 查询接口回放：应看到该 run 且为完成态。
	snapshot := httptest.NewRecorder()
	handler.ServeHTTP(snapshot, httptest.NewRequest(http.MethodGet, "/sessions/"+id+"/execution?run_id="+chat.Data.RunID, nil))
	if snapshot.Code != 200 {
		t.Fatalf("GET execution 状态=%d 响应=%s", snapshot.Code, snapshot.Body.String())
	}
	var snap executionEnvelope
	if err = json.Unmarshal(snapshot.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Data.Runs) != 1 || snap.Data.Runs[0].Status != execution.StatusCompleted {
		t.Fatalf("run 记录不正确: %+v", snap.Data.Runs)
	}
	seen := map[execution.Type]bool{}
	for _, ev := range snap.Data.Events {
		seen[ev.Type] = true
	}
	if !seen[execution.RunStarted] || !seen[execution.RunCompleted] {
		t.Fatalf("查询结果缺少起始或终态事件: %+v", seen)
	}

	// 删除会话后执行记录应一并清理。
	del := httptest.NewRecorder()
	handler.ServeHTTP(del, httptest.NewRequest(http.MethodDelete, "/sessions/"+id, nil))
	if del.Code != 200 {
		t.Fatalf("DELETE 会话状态=%d 响应=%s", del.Code, del.Body.String())
	}
	after := httptest.NewRecorder()
	handler.ServeHTTP(after, httptest.NewRequest(http.MethodGet, "/sessions/"+id+"/execution", nil))
	var empty executionEnvelope
	if err = json.Unmarshal(after.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if len(empty.Data.Runs) != 0 || len(empty.Data.Events) != 0 {
		t.Fatalf("删除会话后仍有执行记录: runs=%+v events=%+v", empty.Data.Runs, empty.Data.Events)
	}
}
