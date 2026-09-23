package multimodal

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"my-eino-app/internal/config"
)

func TestCSVExtractionKeepsRowAndColumnReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.csv")
	if err := os.WriteFile(path, []byte("name,value\nalpha,42\n"), 0600); err != nil {
		t.Fatal(err)
	}
	processor := NewProcessor(config.AttachmentsConfig{})
	artifacts, err := processor.parseCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].SourceRef != "CSV A1:B2" || !strings.Contains(artifacts[0].Text, "A2=\"alpha\"") || !strings.Contains(artifacts[0].Text, "B2=\"42\"") {
		t.Fatalf("CSV evidence lost cell coordinates: %+v", artifacts)
	}
}

func TestXLSXExtractionKeepsSheetCellAndFormulaReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.xlsx")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	parts := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="FY26" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml" Type="worksheet"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>Revenue</t></si></sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1"><v>42</v></c></row><row r="2"><c r="B2"><f>B1*2</f><v>84</v></c></row></sheetData></worksheet>`,
	}
	for name, body := range parts {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	artifacts, err := NewProcessor(config.AttachmentsConfig{}).parseXLSX(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || !strings.Contains(artifacts[0].SourceRef, `sheet "FY26"!A1:B2`) || !strings.Contains(artifacts[0].Text, `A1="Revenue"`) || !strings.Contains(artifacts[0].Text, "formula: =B1*2") {
		t.Fatalf("XLSX evidence lost sheet/cell/formula references: %+v", artifacts)
	}
}

func TestVisionProviderUsesStructuredOutputAndDataURI(t *testing.T) {
	image := []byte("fake-png-data")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected provider request: %s %s", r.Method, r.URL.String())
		}
		var request struct {
			Messages []struct {
				Content []struct {
					Type     string `json:"type"`
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(image)
		if request.Messages[0].Content[1].ImageURL.URL != want {
			t.Errorf("unexpected image source: %q", request.Messages[0].Content[1].ImageURL.URL)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"text":"label: 42","confidence":0.93}`}}}})
	}))
	defer server.Close()
	processor := NewProcessor(config.AttachmentsConfig{Vision: config.MultimodalProvider{Enabled: true, BaseURL: server.URL + "/v1", APIKey: "test-key", Model: "vision-test", Timeout: config.Duration(time.Second)}})
	result, err := processor.vision(context.Background(), image, "image/png", "test image")
	if err != nil {
		t.Fatal(err)
	}
	if result.text != "label: 42" || result.confidence == nil || *result.confidence != 0.93 {
		t.Fatalf("unexpected vision result: %+v", result)
	}
}

func TestVisionProviderRejectsUnstructuredOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"Looks like a receipt"}}]}`)
	}))
	defer server.Close()
	processor := NewProcessor(config.AttachmentsConfig{Vision: config.MultimodalProvider{Enabled: true, BaseURL: server.URL, Model: "vision-test", Timeout: config.Duration(time.Second)}})
	if _, err := processor.vision(context.Background(), []byte("image"), "image/jpeg", "test"); err == nil {
		t.Fatal("unstructured provider response was accepted")
	}
}

func TestVisionProviderRejectsTrailingJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{
			map[string]any{"message": map[string]string{"content": `{"text":"label: 42","confidence":0.93} {"unexpected":true}`}},
		}})
	}))
	defer server.Close()
	processor := NewProcessor(config.AttachmentsConfig{Vision: config.MultimodalProvider{Enabled: true, BaseURL: server.URL, Model: "vision-test", Timeout: config.Duration(time.Second)}})
	if _, err := processor.vision(context.Background(), []byte("image"), "image/png", "test"); err == nil {
		t.Fatal("vision provider output with trailing JSON was accepted")
	}
}

func TestTranscriptionKeepsTimestampedSegments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" || r.Header.Get("Authorization") != "Bearer audio-key" {
			t.Errorf("unexpected transcription request %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		if r.FormValue("model") != "whisper-test" || r.FormValue("response_format") != "verbose_json" {
			t.Errorf("missing transcription options: %+v", r.Form)
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		_ = file.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{"text": "你好", "segments": []any{map[string]any{"start": 1.25, "end": 2.5, "text": "你好", "speaker_id": "speaker_0", "confidence": 0.88}}})
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(path, []byte("audio-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	processor := NewProcessor(config.AttachmentsConfig{Transcription: config.MultimodalProvider{Enabled: true, BaseURL: server.URL + "/v1", APIKey: "audio-key", Model: "whisper-test", Timeout: config.Duration(time.Second)}})
	artifacts, err := processor.transcribe(context.Background(), path, "voice.wav")
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].SourceRef != "audio 1.250-2.500 seconds" || len(artifacts[0].Blocks) != 1 || artifacts[0].Blocks[0].StartTime != 1.25 || !strings.Contains(artifacts[0].Text, "speaker_0") || artifacts[0].Confidence == nil || *artifacts[0].Confidence != 0.88 {
		t.Fatalf("transcript did not retain its timestamp source: %+v", artifacts)
	}
}

func TestProviderURLRejectsCredentialsAndQueries(t *testing.T) {
	for _, raw := range []string{"ftp://example.test/v1", "https://user:pass@example.test/v1", "https://example.test/v1?token=x"} {
		if _, err := providerURL(raw, "chat/completions"); err == nil {
			t.Errorf("unsafe provider URL accepted: %s", raw)
		}
	}
}

func TestPDFTextQualityThresholdAndLowVisionConfidence(t *testing.T) {
	if sufficientPDFText("页 1") || !sufficientPDFText("本页包含完整的扫描文字内容") {
		t.Fatal("PDF text quality gate did not distinguish a nearly empty page")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"text\":\"uncertain\",\"confidence\":0.2}"}}]}`)
	}))
	defer server.Close()
	processor := NewProcessor(config.AttachmentsConfig{Vision: config.MultimodalProvider{Enabled: true, BaseURL: server.URL, Model: "vision-test", Timeout: config.Duration(time.Second)}})
	if _, err := processor.vision(context.Background(), []byte("image"), "image/png", "test"); err == nil {
		t.Fatal("low-confidence vision output was accepted")
	}
}
