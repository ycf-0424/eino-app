package server

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"my-eino-app/internal/session"
	"my-eino-app/internal/skill"
)

func TestWebApplicationIsEmbedded(t *testing.T) {
	service := &Service{}
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	for _, item := range []struct {
		path        string
		contentType string
		contains    string
	}{
		{path: "/", contentType: "text/html", contains: "星河系统"},
		{path: "/app.css", contentType: "text/css", contains: ".app-shell"},
		{path: "/app.js", contentType: "javascript", contains: "new WebSocket"},
	} {
		resp, err := server.Client().Get(server.URL + item.path)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), item.contentType) || !strings.Contains(string(body), item.contains) {
			t.Fatalf("GET %s: status=%d content-type=%q", item.path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
}

func TestHealthAndSessionsEndpoints(t *testing.T) {
	sessions, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{sessions: sessions, skills: skill.NewLoader(t.TempDir())}
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	resp, err := server.Client().Get(server.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("health status=%d", resp.StatusCode)
	}
	resp, err = server.Client().Get(server.URL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("sessions status=%d", resp.StatusCode)
	}
}

func TestWebSocketReady(t *testing.T) {
	sessions, _ := session.New(t.TempDir())
	service := &Service{sessions: sessions, skills: skill.NewLoader(t.TempDir())}
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?session_id=test"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var event map[string]any
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "ready" || event["session_id"] != "test" {
		t.Fatalf("event=%v", event)
	}
	// ready 帧必须明确回传 debug 状态，前端据此决定是否显示技能入口；
	// cfg 为 nil 时属于非调试模式，只能是 false。
	if debug, ok := event["debug"].(bool); !ok || debug {
		t.Fatalf("ready debug=%v", event["debug"])
	}
}

func TestStatsRecordsErrorAndDuration(t *testing.T) {
	service := &Service{concurrency: make(chan struct{}, 1)}
	leave, err := service.enter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	leave(errors.New("failed"))
	stats := service.Stats()
	if stats.Requests != 1 || stats.Errors != 1 || stats.Active != 0 || stats.AverageDurationMS < 0 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestConcurrencyWaitHonorsCancellation(t *testing.T) {
	service := &Service{concurrency: make(chan struct{}, 1)}
	leave, err := service.enter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer leave(nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = service.enter(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("enter error=%v, want deadline exceeded", err)
	}
}

func TestSessionWaitHonorsCancellation(t *testing.T) {
	service := &Service{locks: map[string]chan struct{}{}}
	unlock, err := service.acquireSession(context.Background(), "same-session")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = service.acquireSession(ctx, "same-session"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquireSession error=%v, want deadline exceeded", err)
	}
}
