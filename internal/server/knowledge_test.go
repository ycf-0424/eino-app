package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"my-eino-app/internal/auth"
	"my-eino-app/internal/config"
	"my-eino-app/internal/eino/rag"
)

type knowledgeTestStore struct {
	indexed [][]*schema.Document
	removed [][]string
}

func (s *knowledgeTestStore) Index(_ context.Context, docs []*schema.Document) ([]string, error) {
	s.indexed = append(s.indexed, docs)
	ids := make([]string, 0, len(docs))
	for _, doc := range docs {
		ids = append(ids, doc.ID)
	}
	return ids, nil
}

func (s *knowledgeTestStore) Search(context.Context, string, int) ([]*schema.Document, error) {
	return nil, nil
}

func (s *knowledgeTestStore) DeleteIDs(_ context.Context, ids []string) error {
	s.removed = append(s.removed, ids)
	return nil
}

func newKnowledgeTestService(t *testing.T) (*Service, *knowledgeTestStore, string) {
	t.Helper()
	root := t.TempDir()
	store := &knowledgeTestStore{}
	indexer, err := rag.NewIndexer(store, rag.IndexOptions{
		DocumentDir: root, ManifestPath: filepath.Join(root, ".index-manifest.json"),
		EmbeddingModel: "test", Dimension: 1, ChunkSize: 100, ChunkOverlap: 0,
		Store: "test", Target: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{cfg: &config.Config{RAG: config.RAG{Enabled: true, DocumentDir: root, MaxUploadBytes: 1 << 20}}, knowledgeIndexer: indexer}
	return service, store, root
}

func multipartKnowledgeRequest(t *testing.T, method, target, name, body string) *http.Request {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, body)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, target, &buffer)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.RequestURI = ""
	return req
}

func TestKnowledgeAdminRoutesUploadListDelete(t *testing.T) {
	service, store, root := newKnowledgeTestService(t)
	server := httptest.NewServer(service.Handler())
	defer server.Close()

	req := multipartKnowledgeRequest(t, http.MethodPost, server.URL+"/knowledge/documents", "guide.md", "# Guide\nhello knowledge")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("upload status=%d body=%s", resp.StatusCode, data)
	}
	resp.Body.Close()
	if _, err := os.Stat(filepath.Join(root, "guide.md")); err != nil {
		t.Fatalf("uploaded document missing: %v", err)
	}
	if len(store.indexed) == 0 || len(store.indexed[len(store.indexed)-1]) == 0 {
		t.Fatal("upload should trigger indexing")
	}

	resp, err = server.Client().Get(server.URL + "/knowledge/documents")
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data struct {
			Documents []knowledgeDocument `json:"documents"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(envelope.Data.Documents) != 1 || envelope.Data.Documents[0].Name != "guide.md" {
		t.Fatalf("unexpected documents: %+v", envelope.Data.Documents)
	}

	req, _ = http.NewRequest(http.MethodDelete, server.URL+"/knowledge/documents/guide.md", nil)
	resp, err = server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("delete status=%d body=%s", resp.StatusCode, data)
	}
	resp.Body.Close()
	if _, err := os.Stat(filepath.Join(root, "guide.md")); !os.IsNotExist(err) {
		t.Fatalf("document should be removed, err=%v", err)
	}
	if len(store.removed) == 0 {
		t.Fatal("delete should trigger stale-vector cleanup")
	}
}

func TestKnowledgeRoutesRejectUnsafeNames(t *testing.T) {
	service, _, _ := newKnowledgeTestService(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/knowledge/documents/..%2Fsecret.md", nil)
	service.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsafe name status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestKnowledgeStatusStartsEmpty(t *testing.T) {
	service, _, root := newKnowledgeTestService(t)
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	status := service.knowledgeIndexStatus()
	if status.Ready || !status.LastReport.FinishedAt.Equal(time.Time{}) {
		t.Fatalf("unexpected initial status: %+v", status)
	}
}

func TestKnowledgeRoutesRequireAdministratorWhenAuthEnabled(t *testing.T) {
	service, _, _ := newKnowledgeTestService(t)
	service.cfg.Auth.Enabled = true
	service.authSessions = auth.NewSessions(time.Hour)
	service.authMW = auth.NewMiddleware(service.authSessions)
	normalToken, _, err := service.authSessions.Create("local:normal", "普通用户", "", "local")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/knowledge/documents", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: normalToken})
	rec := httptest.NewRecorder()
	service.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("normal user status=%d body=%s", rec.Code, rec.Body.String())
	}

	adminToken, _, err := service.authSessions.CreateWithAdmin("local:admin", "管理员", "", "local", true)
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/knowledge/documents", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: adminToken})
	rec = httptest.NewRecorder()
	service.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status=%d body=%s", rec.Code, rec.Body.String())
	}
}
