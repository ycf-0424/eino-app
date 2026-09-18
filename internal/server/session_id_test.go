package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// POST /sessions 是前端的会话入口：id 由服务端签发并登记归属，
// 客户端不再自己造 id（app.js 的 makeSessionId 已移除）。
func TestCreateSessionIssuesServerSideID(t *testing.T) {
	service := newTestService(t, nil)
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	client := server.Client()

	resp, err := client.Post(server.URL+"/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /sessions status=%d", resp.StatusCode)
	}
	if envelope.Data.ID == "" {
		t.Fatal("POST /sessions must return a session id")
	}

	// 签发的 id 必须可以被立刻使用，并且出现在会话列表里。
	// 修复前前端是先自己造 id、首次 Save 才落库，新会话在这里会是 404/空列表。
	resp, err = client.Get(server.URL + "/sessions/" + envelope.Data.ID)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET issued session status=%d", resp.StatusCode)
	}
	resp, err = client.Get(server.URL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var found bool
	for _, item := range list.Data {
		if item.ID == envelope.Data.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("issued session missing from list: %+v", list.Data)
	}

	// 两次签发必须得到不同的 id。
	resp, err = client.Post(server.URL+"/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var second struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&second); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if second.Data.ID == envelope.Data.ID {
		t.Fatal("consecutive POST /sessions returned the same id")
	}
}

// resolveSessionID 只接受服务端签发过的 id；空值由服务端生成。
func TestResolveSessionID(t *testing.T) {
	service := newTestService(t, nil)
	ctx := context.Background()

	// 没带 id → 服务端生成，且两次不同。
	first, err := service.resolveSessionID(ctx, "")
	if err != nil || first == "" {
		t.Fatalf("resolve(empty) = %q, %v", first, err)
	}
	second, err := service.resolveSessionID(ctx, "")
	if err != nil || second == first {
		t.Fatalf("resolve(empty) must generate a fresh id: %q, %v", second, err)
	}

	// 尚未登记的 id 必须拒绝，不能让客户端自行选择存储键。
	if _, err := service.resolveSessionID(ctx, "unclaimed-id"); !errors.Is(err, errUnsignedSession) {
		t.Fatalf("resolve(unclaimed) err = %v, want errUnsignedSession", err)
	}

	// 已登记且属于当前身份 → 沿用。
	if err := service.sessions.Create("", "mine"); err != nil {
		t.Fatal(err)
	}
	if got, err := service.resolveSessionID(ctx, "mine"); err != nil || got != "mine" {
		t.Fatalf("resolve(own) = %q, %v", got, err)
	}

	// 非法 id 直接报错，不能落到文件路径拼接里。
	if _, err := service.resolveSessionID(ctx, "../escape"); err == nil {
		t.Fatal("resolve must reject unsafe ids")
	}
}
