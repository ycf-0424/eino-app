package memory

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"io"
	"strings"
)

type ModelExtractor struct {
	Model         model.BaseChatModel
	MaxCandidates int
}

func ParseCandidates(raw string, max int) ([]Candidate, error) {
	if len(raw) > 32768 {
		return nil, errors.New("parse_error")
	}
	var out struct {
		Candidates []Candidate `json:"candidates"`
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil {
		return nil, errors.New("parse_error")
	}
	if d.Decode(new(any)) != io.EOF || out.Candidates == nil || len(out.Candidates) > max {
		return nil, errors.New("parse_error")
	}
	return out.Candidates, nil
}
func (e *ModelExtractor) Extract(ctx context.Context, in ExtractInput) ([]Candidate, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	msgs := []*schema.Message{schema.SystemMessage(ExtractionPrompt), schema.UserMessage(string(raw))}
	for attempt := 0; attempt < 2; attempt++ {
		msg, err := e.Model.Generate(ctx, msgs)
		if err != nil {
			return nil, errors.New("extract_model_error")
		}
		if msg != nil {
			if candidates, err := ParseCandidates(strings.TrimSpace(msg.Content), e.MaxCandidates); err == nil {
				return candidates, nil
			}
		}
		msgs = append(msgs, schema.UserMessage("上次输出结构无效，请仅重新输出规定的 JSON，不附加任何内容。"))
	}
	return nil, errors.New("parse_error")
}
