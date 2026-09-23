// Package multimodal extracts bounded, source-addressable evidence from explicit uploads.
package multimodal

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"my-eino-app/internal/attachments"
	"my-eino-app/internal/config"
	"my-eino-app/internal/eino/rag"
)

const extractorVersion = "phase6-v1"

type Processor struct {
	cfg config.AttachmentsConfig
}

func NewProcessor(cfg config.AttachmentsConfig) *Processor {
	if cfg.ProcessingTimeout <= 0 {
		cfg.ProcessingTimeout = config.Duration(2 * time.Minute)
	}
	if cfg.MaxPages <= 0 {
		cfg.MaxPages = 10
	}
	if cfg.MaxMediaDurationSeconds <= 0 {
		cfg.MaxMediaDurationSeconds = 600
	}
	if cfg.MaxVideoFrames <= 0 {
		cfg.MaxVideoFrames = 12
	}
	if cfg.PDFRenderer == "" {
		cfg.PDFRenderer = "pdftoppm"
	}
	if cfg.FFmpeg == "" {
		cfg.FFmpeg = "ffmpeg"
	}
	if cfg.FFprobe == "" {
		cfg.FFprobe = "ffprobe"
	}
	return &Processor{cfg: cfg}
}

func (p *Processor) Process(ctx context.Context, filePath string, item attachments.Attachment) ([]attachments.Artifact, string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.cfg.ProcessingTimeout))
	defer cancel()
	switch item.MIMEType {
	case "text/plain", "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		sections, err := rag.ParseFile(filePath)
		return p.ragArtifacts(sections, item.MIMEType), extractorVersion, err
	case "text/csv":
		artifacts, err := p.parseCSV(filePath)
		return artifacts, extractorVersion, err
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		artifacts, err := p.parseXLSX(filePath)
		return artifacts, extractorVersion, err
	case "application/pdf":
		artifacts, err := p.parsePDF(ctx, filePath, item)
		return artifacts, extractorVersion, err
	case "image/png", "image/jpeg":
		artifact, err := p.describeImage(ctx, filePath, item.MIMEType, "image")
		if err != nil {
			return nil, extractorVersion, err
		}
		return []attachments.Artifact{artifactFor("image", artifact.text, artifact.confidence)}, extractorVersion, nil
	case "audio/mpeg", "audio/wav", "audio/mp4":
		if _, err := p.mediaDuration(ctx, filePath); err != nil {
			return nil, extractorVersion, err
		}
		artifacts, err := p.transcribe(ctx, filePath, item.OriginalName)
		return artifacts, extractorVersion, err
	case "video/mp4":
		artifacts, err := p.parseVideo(ctx, filePath, item.OriginalName)
		return artifacts, extractorVersion, err
	default:
		return nil, extractorVersion, fmt.Errorf("unsupported attachment MIME type %q", item.MIMEType)
	}
}

func (p *Processor) ragArtifacts(sections []rag.Section, _ string) []attachments.Artifact {
	artifacts := make([]attachments.Artifact, 0, len(sections))
	for i, section := range sections {
		ref := fmt.Sprintf("section %d", i+1)
		if page, ok := section.Metadata["page"]; ok {
			ref = fmt.Sprintf("page %v", page)
		} else if title, ok := section.Metadata["title"].(string); ok && title != "" {
			ref = "section: " + title
		}
		for chunk, content := range rag.SplitText(section.Content, 12000, 200) {
			if strings.TrimSpace(content) == "" {
				continue
			}
			partRef := ref
			if chunk > 0 {
				partRef += fmt.Sprintf(" part %d", chunk+1)
			}
			artifacts = append(artifacts, artifactFor(partRef, content, nil))
		}
	}
	return artifacts
}

func (p *Processor) parsePDF(ctx context.Context, filePath string, item attachments.Attachment) ([]attachments.Artifact, error) {
	pageCount, err := rag.PDFPageCount(filePath)
	if err != nil {
		return nil, fmt.Errorf("read PDF page count: %w", err)
	}
	if pageCount < 1 || pageCount > p.cfg.MaxPages {
		return nil, fmt.Errorf("PDF page count %d exceeds the configured limit %d", pageCount, p.cfg.MaxPages)
	}
	sections, err := rag.ParseFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("extract PDF text: %w", err)
	}
	textPages := make(map[int]bool)
	var artifacts []attachments.Artifact
	var lowQualityPages []int
	for _, section := range sections {
		if page, ok := section.Metadata["page"].(int); ok {
			textPages[page] = true
			if !sufficientPDFText(section.Content) {
				lowQualityPages = append(lowQualityPages, page)
				continue
			}
		}
		artifacts = append(artifacts, p.ragArtifacts([]rag.Section{section}, item.MIMEType)...)
	}
	missing := append([]int(nil), lowQualityPages...)
	for page := 1; page <= pageCount; page++ {
		if !textPages[page] {
			missing = append(missing, page)
		}
	}
	if len(missing) == 0 {
		return artifacts, nil
	}
	if !p.cfg.Vision.Enabled {
		if len(artifacts) > 0 {
			return artifacts, fmt.Errorf("PDF has %d scanned page(s), but the vision/OCR provider is not configured", len(missing))
		}
		return nil, errors.New("scanned PDF requires an enabled vision/OCR provider")
	}
	for _, page := range missing {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		imagePath, cleanup, err := p.renderPDFPage(ctx, filePath, page)
		if err != nil {
			return nil, err
		}
		image, err := os.ReadFile(imagePath)
		cleanup()
		if err != nil {
			return nil, err
		}
		if len(image) > 15<<20 {
			return nil, errors.New("rendered PDF page exceeds the 15 MiB processing limit")
		}
		result, err := p.vision(ctx, image, "image/png", fmt.Sprintf("扫描 PDF 第 %d 页", page))
		if err != nil {
			return nil, fmt.Errorf("OCR page %d: %w", page, err)
		}
		artifacts = append(artifacts, artifactFor(fmt.Sprintf("page %d", page), result.text, result.confidence))
	}
	return artifacts, nil
}

func sufficientPDFText(text string) bool {
	return utf8.RuneCountInString(strings.TrimSpace(text)) >= 12
}

func (p *Processor) renderPDFPage(ctx context.Context, filePath string, page int) (string, func(), error) {
	dir, err := os.MkdirTemp("", "eino-pdf-page-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	prefix := filepath.Join(dir, "page")
	cmd := exec.CommandContext(ctx, p.cfg.PDFRenderer, "-f", strconv.Itoa(page), "-l", strconv.Itoa(page), "-r", "120", "-singlefile", "-png", filePath, prefix)
	if err := cmd.Run(); err != nil {
		cleanup()
		return "", func() {}, errors.New("PDF renderer is unavailable or failed")
	}
	return prefix + ".png", cleanup, nil
}

type visionResult struct {
	text       string
	confidence *float64
}

func (p *Processor) describeImage(ctx context.Context, filePath, mime, source string) (visionResult, error) {
	image, err := os.ReadFile(filePath)
	if err != nil {
		return visionResult{}, err
	}
	if len(image) > 15<<20 {
		return visionResult{}, errors.New("image exceeds the 15 MiB processing limit")
	}
	return p.vision(ctx, image, mime, source)
}

func (p *Processor) vision(ctx context.Context, image []byte, mime, source string) (visionResult, error) {
	if !p.cfg.Vision.Enabled {
		return visionResult{}, errors.New("vision/OCR provider is not configured")
	}
	prompt := "Read this user-provided image and return JSON with exactly two keys: text and confidence. " +
		"text must contain only visible text transcribed faithfully plus concise descriptions of relevant non-text visual evidence; " +
		"do not infer hidden facts. confidence must be a number from 0 to 1 describing extraction confidence. " + source
	body := map[string]any{
		"model":           p.cfg.Vision.Model,
		"temperature":     0,
		"response_format": map[string]string{"type": "json_object"},
		"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]string{"type": "text", "text": prompt},
			map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(image)}},
		}}},
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := p.postJSON(ctx, p.cfg.Vision, "chat/completions", body, &response); err != nil {
		return visionResult{}, err
	}
	if len(response.Choices) == 0 {
		return visionResult{}, errors.New("vision provider returned no choices")
	}
	var parsed struct {
		Text       string   `json:"text"`
		Confidence *float64 `json:"confidence"`
	}
	decoder := json.NewDecoder(strings.NewReader(response.Choices[0].Message.Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return visionResult{}, errors.New("vision provider returned invalid structured output")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return visionResult{}, errors.New("vision provider returned trailing structured output")
	}
	if parsed.Text == "" || len(parsed.Text) > 64<<10 || parsed.Confidence == nil || *parsed.Confidence < 0.4 || *parsed.Confidence > 1 {
		return visionResult{}, errors.New("vision provider output is missing text or a valid confidence score")
	}
	if strings.TrimSpace(parsed.Text) == "" {
		return visionResult{}, errors.New("vision provider found no usable text or visual evidence")
	}
	return visionResult{text: parsed.Text, confidence: parsed.Confidence}, nil
}

func (p *Processor) parseCSV(filePath string) ([]attachments.Artifact, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := csv.NewReader(io.LimitReader(file, 10<<20))
	reader.FieldsPerRecord = -1
	var out strings.Builder
	rows, maxColumns := 0, 0
	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse CSV: %w", err)
		}
		rows++
		if rows > 2000 || len(row) > 100 {
			return nil, errors.New("CSV exceeds the 2000 row / 100 column processing limit")
		}
		if len(row) > maxColumns {
			maxColumns = len(row)
		}
		for column, value := range row {
			fmt.Fprintf(&out, "%s%d=%q; ", columnName(column+1), rows, value)
		}
		out.WriteByte('\n')
		if out.Len() > 64<<10 {
			return nil, errors.New("CSV text exceeds the 64 KiB extraction limit")
		}
	}
	if rows == 0 {
		return nil, errors.New("CSV contains no rows")
	}
	return []attachments.Artifact{artifactFor(fmt.Sprintf("CSV A1:%s%d", columnName(maxColumns), rows), out.String(), nil)}, nil
}

func (p *Processor) parseXLSX(filePath string) ([]attachments.Artifact, error) {
	archive, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, fmt.Errorf("open XLSX: %w", err)
	}
	defer archive.Close()
	files := make(map[string]*zip.File, len(archive.File))
	var totalExpanded uint64
	if len(archive.File) > 256 {
		return nil, errors.New("XLSX contains too many archive parts")
	}
	for _, file := range archive.File {
		name := path.Clean(strings.TrimPrefix(file.Name, "/"))
		if strings.HasPrefix(name, "../") || name == ".." || file.UncompressedSize64 > 16<<20 {
			return nil, errors.New("XLSX contains an unsafe or oversized archive part")
		}
		totalExpanded += file.UncompressedSize64
		if totalExpanded > 40<<20 {
			return nil, errors.New("XLSX expands beyond the 40 MiB processing limit")
		}
		files[name] = file
	}
	workbookData, err := readZipPart(files, "xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	relsData, err := readZipPart(files, "xl/_rels/workbook.xml.rels")
	if err != nil {
		return nil, err
	}
	var workbook struct {
		Sheets []struct {
			Name  string `xml:"name,attr"`
			RelID string `xml:"id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(workbookData, &workbook); err != nil {
		return nil, err
	}
	var relationships struct {
		Items []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := xml.Unmarshal(relsData, &relationships); err != nil {
		return nil, err
	}
	relMap := map[string]string{}
	for _, item := range relationships.Items {
		relMap[item.ID] = item.Target
	}
	var shared []string
	if files["xl/sharedStrings.xml"] != nil {
		shared, err = readSharedStrings(files)
		if err != nil {
			return nil, err
		}
	}
	if len(workbook.Sheets) == 0 || len(workbook.Sheets) > 50 {
		return nil, errors.New("XLSX sheet count is empty or exceeds 50")
	}
	artifacts := make([]attachments.Artifact, 0, len(workbook.Sheets))
	totalCells := 0
	for _, sheet := range workbook.Sheets {
		target := relMap[sheet.RelID]
		if target == "" || strings.Contains(target, "..") || strings.HasPrefix(target, "/") {
			return nil, errors.New("XLSX contains an invalid worksheet reference")
		}
		part := path.Clean(path.Join("xl", target))
		data, err := readZipPart(files, part)
		if err != nil {
			return nil, err
		}
		var worksheet struct {
			Rows []struct {
				Cells []struct {
					Ref     string `xml:"r,attr"`
					Type    string `xml:"t,attr"`
					Formula string `xml:"f"`
					Value   string `xml:"v"`
					Inline  struct {
						Text string `xml:"t"`
						Runs []struct {
							Text string `xml:"t"`
						} `xml:"r"`
					} `xml:"is"`
				} `xml:"c"`
			} `xml:"sheetData>row"`
		}
		if err := xml.Unmarshal(data, &worksheet); err != nil {
			return nil, fmt.Errorf("parse worksheet %q: %w", sheet.Name, err)
		}
		var text strings.Builder
		first, last := "", ""
		for _, row := range worksheet.Rows {
			for _, cell := range row.Cells {
				totalCells++
				if totalCells > 10000 {
					return nil, errors.New("XLSX exceeds the 10000 cell processing limit")
				}
				value := cell.Value
				switch cell.Type {
				case "s":
					index, parseErr := strconv.Atoi(value)
					if parseErr != nil || index < 0 || index >= len(shared) {
						return nil, errors.New("XLSX contains an invalid shared-string index")
					}
					value = shared[index]
				case "inlineStr":
					value = cell.Inline.Text
					for _, run := range cell.Inline.Runs {
						value += run.Text
					}
				}
				if cell.Ref == "" {
					continue
				}
				if first == "" {
					first = cell.Ref
				}
				last = cell.Ref
				if cell.Formula != "" {
					value += " (formula: =" + cell.Formula + ")"
				}
				fmt.Fprintf(&text, "%s=%q\n", cell.Ref, value)
				if text.Len() > 64<<10 {
					return nil, errors.New("worksheet text exceeds the 64 KiB extraction limit")
				}
			}
		}
		if text.Len() > 0 {
			name := safeSheetName(sheet.Name)
			artifacts = append(artifacts, artifactFor(fmt.Sprintf("sheet %q!%s:%s", name, first, last), text.String(), nil))
		}
	}
	if len(artifacts) == 0 {
		return nil, errors.New("XLSX contains no readable cells")
	}
	return artifacts, nil
}

func readZipPart(files map[string]*zip.File, name string) ([]byte, error) {
	item := files[name]
	if item == nil {
		return nil, fmt.Errorf("XLSX part %q is missing", name)
	}
	reader, err := item.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, (16<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 16<<20 {
		return nil, errors.New("XLSX part exceeds the 16 MiB processing limit")
	}
	return data, nil
}

func readSharedStrings(files map[string]*zip.File) ([]string, error) {
	data, err := readZipPart(files, "xl/sharedStrings.xml")
	if err != nil {
		return nil, err
	}
	var stringsXML struct {
		Items []struct {
			Text string `xml:"t"`
			Runs []struct {
				Text string `xml:"t"`
			} `xml:"r"`
		} `xml:"si"`
	}
	if err := xml.Unmarshal(data, &stringsXML); err != nil {
		return nil, err
	}
	values := make([]string, 0, len(stringsXML.Items))
	for _, item := range stringsXML.Items {
		value := item.Text
		for _, run := range item.Runs {
			value += run.Text
		}
		values = append(values, value)
	}
	return values, nil
}

func (p *Processor) mediaDuration(ctx context.Context, filePath string) (float64, error) {
	cmd := exec.CommandContext(ctx, p.cfg.FFprobe, "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", filePath)
	data, err := cmd.Output()
	if err != nil {
		return 0, errors.New("media probe tool is unavailable or failed")
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	if err != nil || duration <= 0 || duration > float64(p.cfg.MaxMediaDurationSeconds) {
		return 0, fmt.Errorf("media duration is invalid or exceeds %d seconds", p.cfg.MaxMediaDurationSeconds)
	}
	return duration, nil
}

func (p *Processor) parseVideo(ctx context.Context, filePath, name string) ([]attachments.Artifact, error) {
	duration, err := p.mediaDuration(ctx, filePath)
	if err != nil {
		return nil, err
	}
	if !p.cfg.Vision.Enabled {
		return nil, errors.New("video frame extraction requires an enabled vision provider")
	}
	if !p.cfg.Transcription.Enabled {
		return nil, errors.New("video audio extraction requires an enabled transcription provider")
	}
	dir, err := os.MkdirTemp("", "eino-video-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	audioPath := filepath.Join(dir, "audio.wav")
	cmd := exec.CommandContext(ctx, p.cfg.FFmpeg, "-hide_banner", "-loglevel", "error", "-i", filePath, "-vn", "-ac", "1", "-ar", "16000", "-t", strconv.Itoa(p.cfg.MaxMediaDurationSeconds), "-y", audioPath)
	if err := cmd.Run(); err != nil {
		return nil, errors.New("video audio extraction failed")
	}
	artifacts, err := p.transcribe(ctx, audioPath, filepath.Base(name)+" audio")
	if err != nil {
		return nil, err
	}
	frameCount := p.cfg.MaxVideoFrames
	if float64(frameCount) > duration {
		frameCount = int(duration)
	}
	for i := 0; i < frameCount; i++ {
		at := duration * float64(i) / float64(max(frameCount, 1))
		framePath := filepath.Join(dir, fmt.Sprintf("frame-%02d.jpg", i+1))
		cmd := exec.CommandContext(ctx, p.cfg.FFmpeg, "-hide_banner", "-loglevel", "error", "-ss", fmt.Sprintf("%.3f", at), "-i", filePath,
			"-frames:v", "1", "-vf", "scale=1280:720:force_original_aspect_ratio=decrease", "-q:v", "4", "-y", framePath)
		if err := cmd.Run(); err != nil {
			return nil, errors.New("video key-frame extraction failed")
		}
		info, err := os.Stat(framePath)
		if err != nil {
			return nil, err
		}
		if info.Size() > 15<<20 {
			return nil, errors.New("video key frame exceeds the processing limit")
		}
		frame, err := os.ReadFile(framePath)
		if err != nil {
			return nil, err
		}
		result, err := p.vision(ctx, frame, "image/jpeg", fmt.Sprintf("视频关键帧时间戳 %.3f 秒", at))
		if err != nil {
			return nil, fmt.Errorf("analyze video frame at %.3f seconds: %w", at, err)
		}
		artifacts = append(artifacts, artifactFor(fmt.Sprintf("video frame %.3f seconds", at), result.text, result.confidence))
	}
	return artifacts, nil
}

func (p *Processor) transcribe(ctx context.Context, filePath, originalName string) ([]attachments.Artifact, error) {
	if !p.cfg.Transcription.Enabled {
		return nil, errors.New("transcription provider is not configured")
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > 25<<20 {
		return nil, errors.New("audio exceeds the 25 MiB transcription limit")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", p.cfg.Transcription.Model); err != nil {
		return nil, err
	}
	if err := writer.WriteField("response_format", "verbose_json"); err != nil {
		return nil, err
	}
	filename := "audio" + strings.ToLower(filepath.Ext(originalName))
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(part, file); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	endpoint, err := providerURL(p.cfg.Transcription.BaseURL, "audio/transcriptions")
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if p.cfg.Transcription.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.cfg.Transcription.APIKey)
	}
	response, err := providerClient(p.cfg.Transcription.Timeout).Do(request)
	if err != nil {
		return nil, errors.New("transcription provider request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("transcription provider returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Text       string   `json:"text"`
		Confidence *float64 `json:"confidence"`
		Segments   []struct {
			Start               float64  `json:"start"`
			End                 float64  `json:"end"`
			Text                string   `json:"text"`
			Speaker             string   `json:"speaker_id"`
			Confidence          *float64 `json:"confidence"`
			NoSpeechProbability *float64 `json:"no_speech_prob"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil || strings.TrimSpace(decoded.Text) == "" {
		return nil, errors.New("transcription provider returned no usable text")
	}
	if len(decoded.Text) > 1<<20 || len(decoded.Segments) > 5000 {
		return nil, errors.New("transcription exceeds the extraction limits")
	}
	if len(decoded.Segments) == 0 {
		artifact := artifactFor("audio transcript", decoded.Text, nil)
		artifact.Confidence = validConfidence(decoded.Confidence)
		return []attachments.Artifact{artifact}, nil
	}
	artifacts := make([]attachments.Artifact, 0, len(decoded.Segments))
	for _, segment := range decoded.Segments {
		if strings.TrimSpace(segment.Text) == "" || segment.Start < 0 || segment.End < segment.Start {
			continue
		}
		text := segment.Text
		if segment.Speaker != "" {
			text = "[" + segment.Speaker + "] " + text
		}
		artifact := artifactFor(fmt.Sprintf("audio %.3f-%.3f seconds", segment.Start, segment.End), text, nil)
		confidence := segment.Confidence
		if segment.NoSpeechProbability != nil {
			derived := 1 - *segment.NoSpeechProbability
			confidence = &derived
		}
		artifact.Confidence = validConfidence(confidence)
		artifact.Blocks = []attachments.Block{{Kind: "transcript_segment", Text: text, StartTime: segment.Start, EndTime: segment.End}}
		artifacts = append(artifacts, artifact)
	}
	if len(artifacts) == 0 {
		return nil, errors.New("transcription provider returned invalid timestamp segments")
	}
	return artifacts, nil
}

func validConfidence(value *float64) *float64 {
	if value == nil || *value < 0 || *value > 1 {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func (p *Processor) postJSON(ctx context.Context, provider config.MultimodalProvider, route string, payload any, target any) error {
	endpoint, err := providerURL(provider.BaseURL, route)
	if err != nil {
		return err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if provider.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+provider.APIKey)
	}
	response, err := providerClient(provider.Timeout).Do(request)
	if err != nil {
		return errors.New("multimodal provider request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("multimodal provider returned HTTP %d", response.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func providerURL(base, route string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("multimodal provider base_url must be an absolute HTTP(S) URL without credentials or query")
	}
	return strings.TrimRight(base, "/") + "/" + route, nil
}

func providerClient(timeout config.Duration) *http.Client {
	d := time.Duration(timeout)
	if d <= 0 {
		d = 60 * time.Second
	}
	return &http.Client{Timeout: d, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}

func artifactFor(source, text string, confidence *float64) attachments.Artifact {
	hash := sha256.Sum256([]byte(text))
	return attachments.Artifact{SourceRef: source, Text: strings.TrimSpace(text), OCRConfidence: confidence, ExtractorVersion: extractorVersion, ContentHash: fmt.Sprintf("%x", hash[:])}
}

func columnName(n int) string {
	var out string
	for n > 0 {
		n--
		out = string(rune('A'+n%26)) + out
		n /= 26
	}
	return out
}

func safeSheetName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 {
			return ' '
		}
		return r
	}, name)
	if len(name) > 120 {
		name = string([]rune(name)[:120])
	}
	return name
}

// Keep multipart/mime references visible to static analysis while ensuring no
// request is ever redirected to a provider-selected destination.
var _ = multipart.ErrMessageTooLarge
