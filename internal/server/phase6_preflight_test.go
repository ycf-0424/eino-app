package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"my-eino-app/internal/config"
	"my-eino-app/internal/eino/tool"
	"my-eino-app/internal/execution"
)

func TestExplicitLocalFilePath(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
		want  string
	}{
		{name: "root-relative path", query: "请读取 workspace-files/README.txt 并总结", want: filepath.Join("workspace-files", "README.txt")},
		{name: "natural language path", query: "请读取 workspace-files 里的 README.txt 并总结", want: filepath.Join("workspace-files", "README.txt")},
		{name: "quoted path with spaces", query: `请读取 "workspace-files/Project Notes/README.txt"`, want: filepath.Join("workspace-files", "Project Notes", "README.txt")},
		{name: "absolute path under configured root", query: `请读取 E:\repo\workspace-files\README.txt`, want: `E:\repo\workspace-files\README.txt`},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := explicitLocalFilePath(test.query, []string{"workspace-files"})
			if !ok || filepath.Clean(got) != filepath.Clean(test.want) {
				t.Fatalf("explicitLocalFilePath() = %q, %t; want %q, true", got, ok, test.want)
			}
		})
	}

	absoluteRoot := filepath.Join(t.TempDir(), "workspace-files")
	got, ok := explicitLocalFilePath("请读取 workspace-files/README.TXT", []string{absoluteRoot})
	want := filepath.Join(absoluteRoot, "README.TXT")
	if !ok || !strings.EqualFold(filepath.Clean(got), filepath.Clean(want)) {
		t.Fatalf("absolute configured root alias = %q, %t; want %q, true", got, ok, want)
	}
	got, ok = explicitLocalFilePath("请读取 workspace-files 里的 README.TXT", []string{absoluteRoot})
	if !ok || !strings.EqualFold(filepath.Clean(got), filepath.Clean(want)) {
		t.Fatalf("natural-language absolute root alias = %q, %t; want %q, true", got, ok, want)
	}
	if got, ok = explicitLocalFilePath("请读取 https://example.com/workspace-files/README.txt", []string{absoluteRoot}); ok {
		t.Fatalf("URL path unexpectedly selected local file %q", got)
	}

	for _, query := range []string{
		"workspace-files 目录里有什么？",
		"如何读取 workspace-files/README.txt？",
		"读取 workspace-files-other/README.txt",
		"读取 C:/private/README.txt",
	} {
		path, named := explicitLocalFilePath(query, []string{"workspace-files"})
		if named && explicitlyRequestsLocalFileRead(query) {
			t.Errorf("unqualified or non-read request unexpectedly selected %q from %q", path, query)
		}
	}
}

func TestExplicitLocalFilePathUsesLoadedProjectRootAlias(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("project config unavailable: %v", err)
	}
	got, ok := explicitLocalFilePath("请读取 workspace-files/README.txt 并总结", cfg.LocalFiles.Roots)
	if !ok {
		t.Fatalf("loaded roots %v did not recognize workspace-files alias", cfg.LocalFiles.Roots)
	}
	want := filepath.Join(cfg.ProjectDir, "workspace-files", "README.txt")
	if !ok || !strings.EqualFold(filepath.Clean(got), filepath.Clean(want)) {
		t.Fatalf("loaded root alias resolved to %q, want %q", got, want)
	}
	query := "读取 workspace-files/README.txt，若要开放其他目录需要修改哪里？"
	got, ok = explicitLocalFilePath(query, cfg.LocalFiles.Roots)
	if !ok || !strings.EqualFold(filepath.Clean(got), filepath.Clean(want)) || !explicitlyRequestsLocalFileRead(query) {
		t.Fatalf("loaded root did not preflight exact file question %q: path=%q ok=%t", query, got, ok)
	}
}

func TestExplicitlyRequestsLocalFileReadRespectsNegationAndHowToRequests(t *testing.T) {
	for _, test := range []struct {
		query string
		want  bool
	}{
		{query: "请读取 workspace-files/README.txt", want: true},
		{query: "summarize workspace-files/README.txt", want: true},
		{query: "README.txt 的 readme 格式表示什么？", want: false},
		{query: "workspace-files/README.txt 路径格式表示什么？", want: false},
		{query: "不要读取 workspace-files/README.txt", want: false},
		{query: "how to read workspace-files/README.txt", want: false},
		{query: "do not read workspace-files/README.txt", want: false},
		{query: "read workspace-files/README.txt, then delete it", want: false},
	} {
		if got := explicitlyRequestsLocalFileRead(test.query); got != test.want {
			t.Errorf("explicitlyRequestsLocalFileRead(%q) = %t, want %t", test.query, got, test.want)
		}
	}
}

func TestLocalFilePreflightEmitsAuditableEvents(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.txt"), []byte("正文内容"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, err := tool.NewLocalFileReadTool([]string{root}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{localFiles: reader}
	recorder := execution.NewRecorder(0)
	emitter := execution.NewSession("run-success", "session", nil, recorder, execution.SessionOptions{})
	ctx := execution.WithEmitter(context.Background(), emitter)
	result, err := s.runLocalFilePreflight(ctx, filepath.Join(root, "README.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "README.txt" || !strings.Contains(result.Answer, "正文内容") || strings.Contains(result.Answer, root) {
		t.Fatalf("preflight result leaked path or lost evidence: %+v", result)
	}
	events := recorder.Events()
	if len(events) != 2 || events[0].Type != execution.ToolStarted || events[1].Type != execution.FileReadDone {
		t.Fatalf("unexpected successful read events: %+v", events)
	}
	if events[0].Payload["source"] != "server_preflight" || events[1].Payload["file_name"] != "README.txt" {
		t.Fatalf("missing source evidence: %+v", events)
	}

	failedRecorder := execution.NewRecorder(0)
	failedEmitter := execution.NewSession("run-failed", "session", nil, failedRecorder, execution.SessionOptions{})
	failedCtx := execution.WithEmitter(context.Background(), failedEmitter)
	if _, err = s.runLocalFilePreflight(failedCtx, filepath.Join(root, "missing.txt")); err == nil {
		t.Fatal("missing file unexpectedly read successfully")
	}
	failedEvents := failedRecorder.Events()
	if len(failedEvents) != 2 || failedEvents[0].Type != execution.ToolStarted || failedEvents[1].Type != execution.ToolFailed || failedEvents[1].Payload["error_code"] != "outside_roots" {
		t.Fatalf("unexpected failed read events: %+v", failedEvents)
	}
}

func TestAppendEvidenceCitation(t *testing.T) {
	history := []*schema.Message{schema.AssistantMessage("结论：Milvus 是当前向量库。", nil)}
	suffix := appendEvidenceCitation(history, []string{"docs/milvus.md", "README.txt", "README.txt"})
	wantSuffix := "\n\n来源：milvus.md、README.txt"
	if suffix != wantSuffix || history[0].Content != "结论：Milvus 是当前向量库。"+wantSuffix {
		t.Fatalf("citation suffix=%q history=%+v", suffix, history)
	}
	if got := appendEvidenceCitation(history, []string{"milvus.md", "README.txt"}); got != "" {
		t.Fatalf("already cited sources should not be repeated: %q", got)
	}
	if history[0].Role != schema.Assistant {
		t.Fatalf("assistant history unexpectedly changed role: %v", history[0].Role)
	}
}

func TestLocalFilePreflightFailureDoesNotAskModelToGuess(t *testing.T) {
	var modelCalls atomic.Int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"unused\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"should not be called\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"unused\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer modelServer.Close()

	root := t.TempDir()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Models = nil
	cfg.ActiveModel = ""
	cfg.OpenAI.BaseURL = modelServer.URL + "/v1"
	cfg.OpenAI.APIKey = "test"
	cfg.Session.Store = "file"
	cfg.SessionDir = t.TempDir()
	cfg.Agent.CheckpointDir = t.TempDir()
	cfg.Agent.MultiAgent = false
	cfg.Agent.AutoRouting.Enabled = false
	cfg.RAG.Enabled = false
	cfg.Memory.Enabled = false
	cfg.LocalFiles = config.LocalFiles{Enabled: true, Roots: []string{root}, MaxBytes: 1024}
	cfg.IntentRouting.Enabled = true
	cfg.Auth.Enabled = false
	cfg.ExecutionEvents = config.ExecutionEvents{Enabled: true, Dir: t.TempDir()}
	cfg.Retry.MaxAttempts = 1
	cfg.Runtime.RequestTimeout = config.Duration(10 * time.Second)
	service, err := NewService(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	requestedPath := filepath.Join(root, "missing.txt")
	var answer strings.Builder
	recorder := execution.NewRecorder(0)
	if _, err = service.ChatWithSink(context.Background(), "missing-local-file", "请读取 "+requestedPath+" 并总结", "", "", &answer, recorder); err != nil {
		t.Fatal(err)
	}
	if modelCalls.Load() != 0 {
		t.Fatalf("model was called %d times after the authoritative file read failed", modelCalls.Load())
	}
	if got := answer.String(); !strings.Contains(got, "不会推测或编造") || strings.Contains(got, "should not be called") {
		t.Fatalf("failed local read should produce an honest server response: %q", got)
	}
	var sawIntent, sawFailure bool
	for _, event := range recorder.Events() {
		if event.Type == execution.IntentClassified {
			kind := fmt.Sprint(event.Payload["kind"])
			reason, _ := event.Payload["reason_code"].(string)
			needsClarification, hasClarification := event.Payload["needs_clarification"].(bool)
			sawIntent = kind == "file_understanding" && reason == "explicit_local_file_path" && hasClarification && !needsClarification
		}
		if event.Type == execution.ToolFailed {
			toolName, _ := event.Payload["tool_name"].(string)
			source, _ := event.Payload["source"].(string)
			sawFailure = toolName == "local_file_read" && source == "server_preflight"
		}
	}
	if !sawIntent || !sawFailure {
		t.Fatalf("expected file intent and failed preflight audit events (intent=%t failure=%t); got %+v", sawIntent, sawFailure, recorder.Events())
	}
}

func TestLocalFilePreflightKeepsFilesystemToolServerSide(t *testing.T) {
	var mu sync.Mutex
	var toolNames []string
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode model request: %v", err)
		}
		mu.Lock()
		for _, item := range request.Tools {
			toolNames = append(toolNames, item.Function.Name)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"ok\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"已根据授权文件正文整理。\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"ok\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer modelServer.Close()

	root := filepath.Join(t.TempDir(), "workspace-files")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.txt"), []byte("授权目录正文"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Models = nil
	cfg.ActiveModel = ""
	cfg.OpenAI.BaseURL = modelServer.URL + "/v1"
	cfg.OpenAI.APIKey = "test"
	cfg.Session.Store = "file"
	cfg.SessionDir = t.TempDir()
	cfg.Agent.CheckpointDir = t.TempDir()
	cfg.Agent.MultiAgent = false
	cfg.Agent.AutoRouting.Enabled = false
	cfg.RAG.Enabled = false
	cfg.Memory.Enabled = false
	cfg.LocalFiles = config.LocalFiles{Enabled: true, Roots: []string{root}, MaxBytes: 1024}
	cfg.IntentRouting.Enabled = true
	cfg.Auth.Enabled = false
	cfg.ExecutionEvents = config.ExecutionEvents{Enabled: true, Dir: t.TempDir()}
	cfg.Retry.MaxAttempts = 1
	cfg.Runtime.RequestTimeout = config.Duration(10 * time.Second)
	service, err := NewService(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	for i, query := range []string{
		"请读取 workspace-files/README.txt 并总结",
		"读取 workspace-files/README.txt，若要开放其他目录需要修改哪里？",
	} {
		var answer strings.Builder
		recorder := execution.NewRecorder(0)
		if _, err = service.ChatWithSink(context.Background(), fmt.Sprintf("local-file-server-preflight-%d", i), query, "", "", &answer, recorder); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(answer.String(), "已根据授权文件正文整理") {
			t.Fatalf("unexpected model answer: %q", answer.String())
		}
		var readCompleted bool
		for _, event := range recorder.Events() {
			if event.Type == execution.FileReadDone && event.Payload["file_name"] == "README.txt" && event.Payload["source"] == "server_preflight" {
				readCompleted = true
			}
		}
		if !readCompleted {
			t.Fatalf("expected server-side file preflight event for %q, got %+v", query, recorder.Events())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, name := range toolNames {
		if name == "local_file_read" || name == "write_note" {
			t.Fatalf("unrequested side-effect/filesystem tool was exposed to the model: %v", toolNames)
		}
	}
}
