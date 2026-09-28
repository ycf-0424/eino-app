package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"my-eino-app/internal/auth"
	"my-eino-app/internal/eino/rag"
)

// knowledgeDocument is the user-facing inventory entry. The vector store is
// deliberately not exposed here; source files are the auditable authority.
type knowledgeDocument struct {
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
	Format     string    `json:"format"`
}

type knowledgeIndexState struct {
	Ready      bool            `json:"ready"`
	LastError  string          `json:"last_error,omitempty"`
	LastReport rag.IndexReport `json:"last_report"`
}

var supportedKnowledgeFormats = map[string]bool{
	".md": true, ".txt": true, ".html": true, ".htm": true,
	".json": true, ".docx": true, ".pdf": true,
}

func (s *Service) knowledgeEnabled() bool {
	return s != nil && s.knowledgeIndexer != nil && s.cfg != nil && s.cfg.RAG.Enabled
}

func (s *Service) knowledgeRoot() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	return s.cfg.RAG.DocumentDir
}

func (s *Service) knowledgeMaxUploadBytes() int64 {
	if s == nil || s.cfg == nil || s.cfg.RAG.MaxUploadBytes <= 0 {
		return 25 << 20
	}
	return s.cfg.RAG.MaxUploadBytes
}

func (s *Service) knowledgeIndexStatus() knowledgeIndexState {
	s.knowledgeMu.RLock()
	defer s.knowledgeMu.RUnlock()
	return knowledgeIndexState{
		Ready:      s.knowledgeReport.FinishedAt.After(time.Time{}),
		LastError:  s.knowledgeIndexError,
		LastReport: s.knowledgeReport,
	}
}

func (s *Service) runKnowledgeIndex(r *http.Request, force bool) (rag.IndexReport, error) {
	if !s.knowledgeEnabled() {
		return rag.IndexReport{}, errors.New("knowledge ingestion is not enabled")
	}
	var report rag.IndexReport
	var err error
	if force {
		report, err = s.knowledgeIndexer.RunForce(r.Context())
	} else {
		report, err = s.knowledgeIndexer.Run(r.Context())
	}
	s.knowledgeMu.Lock()
	if err != nil {
		s.knowledgeIndexError = err.Error()
	} else {
		s.knowledgeReport = report
		s.knowledgeIndexError = ""
	}
	s.knowledgeMu.Unlock()
	return report, err
}

func (s *Service) handleKnowledgeList(w http.ResponseWriter, r *http.Request) {
	if !s.knowledgeEnabled() {
		writeJSON(w, http.StatusNotFound, response{Error: "knowledge base is not enabled"})
		return
	}
	items, err := listKnowledgeDocuments(s.knowledgeRoot())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, response{Error: "could not list knowledge documents"})
		return
	}
	writeJSON(w, http.StatusOK, response{Data: map[string]any{
		"documents":        items,
		"index":            s.knowledgeIndexStatus(),
		"max_upload_bytes": s.knowledgeMaxUploadBytes(),
	}})
}

func (s *Service) handleKnowledgeUpload(w http.ResponseWriter, r *http.Request) {
	if !s.knowledgeEnabled() {
		writeJSON(w, http.StatusNotFound, response{Error: "knowledge base is not enabled"})
		return
	}
	maxBytes := s.knowledgeMaxUploadBytes()
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+2<<20)
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, response{Error: "expected multipart/form-data with one file field"})
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		files = r.MultipartForm.File["files"]
	}
	if len(files) != 1 {
		writeJSON(w, http.StatusBadRequest, response{Error: "exactly one knowledge document is required"})
		return
	}
	header := files[0]
	name := filepath.Base(strings.TrimSpace(header.Filename))
	if err := validateKnowledgeName(name); err != nil {
		writeJSON(w, http.StatusBadRequest, response{Error: err.Error()})
		return
	}
	if header.Size > maxBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, response{Error: fmt.Sprintf("knowledge document exceeds %d bytes", maxBytes)})
		return
	}
	src, err := header.Open()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, response{Error: "could not open uploaded knowledge document"})
		return
	}
	defer src.Close()
	root := s.knowledgeRoot()
	if err := os.MkdirAll(root, 0o750); err != nil {
		writeJSON(w, http.StatusInternalServerError, response{Error: "could not create knowledge directory"})
		return
	}
	target := filepath.Join(root, name)
	if err := ensureKnowledgePath(root, target, false); err != nil {
		writeJSON(w, http.StatusBadRequest, response{Error: err.Error()})
		return
	}
	tmp, err := os.CreateTemp(root, ".knowledge-upload-*"+filepath.Ext(name))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, response{Error: "could not create upload staging file"})
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, io.LimitReader(src, maxBytes+1)); err != nil {
		_ = tmp.Close()
		writeJSON(w, http.StatusBadRequest, response{Error: "could not save uploaded knowledge document"})
		return
	}
	if info, statErr := tmp.Stat(); statErr != nil || info.Size() > maxBytes {
		_ = tmp.Close()
		writeJSON(w, http.StatusRequestEntityTooLarge, response{Error: fmt.Sprintf("knowledge document exceeds %d bytes", maxBytes)})
		return
	}
	if err := tmp.Chmod(0o640); err != nil {
		_ = tmp.Close()
		writeJSON(w, http.StatusInternalServerError, response{Error: "could not protect uploaded document"})
		return
	}
	if err := tmp.Close(); err != nil {
		writeJSON(w, http.StatusInternalServerError, response{Error: "could not close uploaded document"})
		return
	}
	if _, err := rag.ParseFile(tmpName); err != nil {
		writeJSON(w, http.StatusBadRequest, response{Error: "uploaded document could not be parsed: " + err.Error()})
		return
	}
	if err := replaceKnowledgeFile(tmpName, target); err != nil {
		writeJSON(w, http.StatusInternalServerError, response{Error: "could not activate uploaded document"})
		return
	}
	report, indexErr := s.runKnowledgeIndex(r, false)
	data := map[string]any{"document": knowledgeDocumentFromPath(root, target), "index": report}
	if indexErr != nil {
		writeJSON(w, http.StatusBadGateway, response{Data: data, Error: "document saved but indexing failed: " + indexErr.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, response{Data: data})
}

func (s *Service) handleKnowledgeDelete(w http.ResponseWriter, r *http.Request) {
	if !s.knowledgeEnabled() {
		writeJSON(w, http.StatusNotFound, response{Error: "knowledge base is not enabled"})
		return
	}
	name := r.PathValue("name")
	if err := validateKnowledgeName(name); err != nil {
		writeJSON(w, http.StatusBadRequest, response{Error: err.Error()})
		return
	}
	target := filepath.Join(s.knowledgeRoot(), name)
	if err := ensureKnowledgePath(s.knowledgeRoot(), target, true); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, response{Error: "knowledge document not found"})
			return
		}
		writeJSON(w, http.StatusBadRequest, response{Error: err.Error()})
		return
	}
	if err := os.Remove(target); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, response{Error: "knowledge document not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, response{Error: "could not delete knowledge document"})
		return
	}
	report, indexErr := s.runKnowledgeIndex(r, false)
	data := map[string]any{"deleted": name, "index": report}
	if indexErr != nil {
		writeJSON(w, http.StatusBadGateway, response{Data: data, Error: "document deleted but indexing failed: " + indexErr.Error()})
		return
	}
	writeJSON(w, http.StatusOK, response{Data: data})
}

func (s *Service) handleKnowledgeReindex(w http.ResponseWriter, r *http.Request) {
	if !s.knowledgeEnabled() {
		writeJSON(w, http.StatusNotFound, response{Error: "knowledge base is not enabled"})
		return
	}
	force := strings.EqualFold(r.URL.Query().Get("force"), "true")
	report, err := s.runKnowledgeIndex(r, force)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, response{Data: report, Error: "knowledge indexing failed: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, response{Data: report})
}

// handleDocumentDownload serves only artifacts produced by document_write.
// The artifact directory is bounded and, for authenticated users, namespaced
// by the same owner hash used by the writer.
func (s *Service) handleDocumentDownload(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.cfg == nil || !s.cfg.DocumentTools.Enabled {
		writeJSON(w, http.StatusNotFound, response{Error: "document writing is not enabled"})
		return
	}
	name := filepath.Base(strings.TrimSpace(r.PathValue("name")))
	if name == "" || name == "." || strings.Contains(name, "..") || !strings.EqualFold(filepath.Ext(name), ".docx") {
		writeJSON(w, http.StatusBadRequest, response{Error: "invalid document artifact name"})
		return
	}
	root := s.cfg.DocumentTools.OutputDir
	if owner := auth.OwnerFromContext(r.Context()); owner != "" {
		root = filepath.Join(root, "owners", knowledgeOwnerKey(owner))
	}
	target := filepath.Join(root, name)
	if err := ensureDownloadPath(root, target); err != nil {
		writeJSON(w, http.StatusNotFound, response{Error: "document artifact not found"})
		return
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		writeJSON(w, http.StatusNotFound, response{Error: "document artifact not found"})
		return
	}
	file, err := os.Open(target)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, response{Error: "could not open document artifact"})
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func ensureDownloadPath(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("artifact path is outside output directory")
	}
	resolved, err := filepath.EvalSymlinks(targetAbs)
	if err != nil {
		return err
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return err
	}
	resolvedRel, err := filepath.Rel(rootResolved, resolved)
	if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) || filepath.IsAbs(resolvedRel) {
		return errors.New("artifact symlink is outside output directory")
	}
	return nil
}

func listKnowledgeDocuments(root string) ([]knowledgeDocument, error) {
	var items []knowledgeDocument
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() == ".index-manifest.json" || strings.HasPrefix(entry.Name(), ".knowledge-") {
			return nil
		}
		if !supportedKnowledgeFormats[strings.ToLower(filepath.Ext(entry.Name()))] {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		items = append(items, knowledgeDocument{Name: filepath.ToSlash(rel), Size: info.Size(), ModifiedAt: info.ModTime().UTC(), Format: strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")})
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return []knowledgeDocument{}, nil
	}
	return items, err
}

func knowledgeDocumentFromPath(root, path string) knowledgeDocument {
	info, err := os.Stat(path)
	if err != nil {
		return knowledgeDocument{Name: filepath.Base(path)}
	}
	rel, _ := filepath.Rel(root, path)
	return knowledgeDocument{Name: filepath.ToSlash(rel), Size: info.Size(), ModifiedAt: info.ModTime().UTC(), Format: strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")}
}

func validateKnowledgeName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || filepath.Base(name) != name || strings.Contains(name, "..") {
		return errors.New("knowledge document name must be a simple filename")
	}
	if !supportedKnowledgeFormats[strings.ToLower(filepath.Ext(name))] {
		return errors.New("unsupported knowledge document format")
	}
	return nil
}

func ensureKnowledgePath(root, target string, mustExist bool) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("knowledge document path is outside the configured directory")
	}
	if mustExist {
		info, err := os.Stat(targetAbs)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("knowledge document is not a regular file")
		}
		resolved, err := filepath.EvalSymlinks(targetAbs)
		if err != nil {
			return err
		}
		rootResolved, err := filepath.EvalSymlinks(rootAbs)
		if err != nil {
			return err
		}
		resolvedRel, err := filepath.Rel(rootResolved, resolved)
		if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) || filepath.IsAbs(resolvedRel) {
			return errors.New("knowledge document symlink is outside the configured directory")
		}
	}
	return nil
}

func replaceKnowledgeFile(tmp, target string) error {
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		return os.Rename(tmp, target)
	} else if err != nil {
		return err
	}
	backup := target + ".previous"
	_ = os.Remove(backup)
	if err := os.Rename(target, backup); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Rename(backup, target)
		return err
	}
	_ = os.Remove(backup)
	return nil
}

func knowledgeOwnerKey(owner string) string {
	sum := sha256.Sum256([]byte(owner))
	return hex.EncodeToString(sum[:8])
}
