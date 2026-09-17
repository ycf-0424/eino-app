// Package evaluation 计算固定 RAG 问答集的正确率、引用率、拒答率和平均耗时。
package evaluation

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"
	"unicode"

	"my-eino-app/internal/eino/chain"
)

// Case 类型。
type Case struct {
	Question     string `json:"question"`
	Expected     string `json:"expected"`
	MustCite     string `json:"must_cite"`
	ShouldRefuse bool   `json:"should_refuse"`
}

// CaseResult 类型。
type CaseResult struct {
	Question   string `json:"question"`
	Answer     string `json:"answer"`
	Correct    bool   `json:"correct"`
	Cited      bool   `json:"cited"`
	Refused    bool   `json:"refused"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// Report 类型。
type Report struct {
	Total             int          `json:"total"`
	CorrectRate       float64      `json:"correct_rate"`
	CitationRate      float64      `json:"citation_rate"`
	RefusalRate       float64      `json:"refusal_rate"`
	AverageDurationMS float64      `json:"average_duration_ms"`
	Cases             []CaseResult `json:"cases"`
}

// LoadCases 函数。
func LoadCases(path string) ([]Case, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []Case
	err = json.Unmarshal(data, &cases)
	return cases, err
}

// Run 逐题执行真实 RAG Chain。指标使用固定规则计算，结果可重复比较。
func Run(ctx context.Context, runner *chain.RAGChain, cases []Case, input func(string) chain.Input) Report {
	report := Report{Total: len(cases)}
	correct, citationRequired, cited, refusalRequired, refused, totalMS := 0, 0, 0, 0, 0, int64(0)
	for _, item := range cases {
		// 指标分母由评测集定义，不能因为一次请求报错就跳过，否则会虚高。
		if item.MustCite != "" {
			citationRequired++
		}
		if item.ShouldRefuse {
			refusalRequired++
		}
		started := time.Now()
		answer, err := runner.Run(ctx, input(item.Question))
		result := CaseResult{Question: item.Question, Answer: answer.Answer, DurationMS: time.Since(started).Milliseconds()}
		totalMS += result.DurationMS
		if err != nil {
			result.Error = err.Error()
		} else {
			result.Refused = strings.Contains(answer.Answer, "无法确认") || strings.Contains(answer.Answer, "不知道") || strings.Contains(answer.Answer, "未知") || strings.Contains(answer.Answer, "无足够依据")
			if item.ShouldRefuse {
				result.Correct = result.Refused
			} else {
				result.Correct = strings.Contains(normalize(answer.Answer), normalize(item.Expected))
			}
			if item.MustCite != "" {
				for _, source := range answer.Sources {
					if strings.Contains(source, item.MustCite) {
						result.Cited = true
						break
					}
				}
			}
			if result.Correct {
				correct++
			}
			if result.Cited {
				cited++
			}
			if item.ShouldRefuse {
				if result.Refused {
					refused++
				}
			}
		}
		report.Cases = append(report.Cases, result)
	}
	if report.Total > 0 {
		report.CorrectRate = float64(correct) / float64(report.Total)
		report.AverageDurationMS = float64(totalMS) / float64(report.Total)
	}
	if citationRequired > 0 {
		report.CitationRate = float64(cited) / float64(citationRequired)
	}
	if refusalRequired > 0 {
		report.RefusalRate = float64(refused) / float64(refusalRequired)
	}
	return report
}

func normalize(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || strings.ContainsRune("，。、；：,.!?！？-—和与", r) {
			return -1
		}
		return unicode.ToLower(r)
	}, value)
}
