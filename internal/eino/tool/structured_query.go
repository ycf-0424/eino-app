package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"my-eino-app/internal/auth"
	"my-eino-app/internal/dataquery"
	"my-eino-app/internal/execution"
)

type StructuredQueryInput struct {
	Source        string `json:"source"`
	Operation     string `json:"operation"`
	ArgumentsJSON string `json:"arguments_json"`
}

// NewStructuredQueryTool exposes only server-registered, read-only operations.
// It intentionally accepts an opaque JSON object as a string; the registry
// performs the authoritative parameter schema validation.
func NewStructuredQueryTool(registry *dataquery.Registry) (einotool.InvokableTool, error) {
	if registry == nil || !registry.Enabled() {
		return nil, dataquery.ErrDisabled
	}
	info := &schema.ToolInfo{
		Name: "structured_query",
		Desc: "Run a registered read-only query against an internal data source. Use only listed source and operation IDs; arguments_json must contain only documented parameters. Never provide SQL, URLs, owner IDs, or credentials.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"source":         {Desc: "Registered data-source identifier", Type: schema.String, Required: true},
			"operation":      {Desc: "Registered read-only operation identifier", Type: schema.String, Required: true},
			"arguments_json": {Desc: "JSON object containing only registered operation parameters", Type: schema.String, Required: true},
		}),
	}
	return utils.NewTool(info, func(ctx context.Context, input StructuredQueryInput) (string, error) {
		if len(input.ArgumentsJSON) == 0 || len(input.ArgumentsJSON) > 8192 {
			return "", fmt.Errorf("structured query arguments exceed limits")
		}
		decoder := json.NewDecoder(bytes.NewBufferString(input.ArgumentsJSON))
		decoder.UseNumber()
		var args map[string]any
		if err := decoder.Decode(&args); err != nil || args == nil {
			return "", fmt.Errorf("arguments_json must be a JSON object")
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return "", fmt.Errorf("arguments_json must contain one JSON object")
		}
		callID := uuid.NewString()
		emitter := execution.FromContext(ctx)
		emitter.Emit(execution.Event{Type: execution.DataQueryStarted, Payload: map[string]any{
			"query_id": callID, "source": input.Source, "operation": input.Operation,
			"args_digest": execution.DigestArgs(input.ArgumentsJSON),
		}})
		result, err := registry.Execute(ctx, dataquery.QueryRequest{
			Source: input.Source, Operation: input.Operation, Arguments: args,
			OwnerID: auth.OwnerFromContext(ctx),
		})
		if err != nil {
			emitter.Emit(execution.Event{Type: execution.DataQueryFailed, Payload: map[string]any{
				"query_id": callID, "source": input.Source, "operation": input.Operation,
				"error_code": dataQueryErrorCode(err),
			}})
			return "", err
		}
		emitter.Emit(execution.Event{Type: execution.DataQueryCompleted, Payload: map[string]any{
			"query_id": callID, "source": result.Source, "operation": result.Operation,
			"row_count": len(result.Rows), "as_of": result.AsOf, "fresh_until": result.FreshUntil,
			"redactions": result.Redactions,
		}})
		encoded, err := json.Marshal(result)
		if err != nil {
			return "", fmt.Errorf("encode query result: %w", err)
		}
		return string(encoded), nil
	}), nil
}

func dataQueryErrorCode(err error) string {
	switch {
	case errors.Is(err, dataquery.ErrForeignOwner):
		return "owner_mismatch"
	case errors.Is(err, dataquery.ErrStaleResult):
		return "stale_data"
	case errors.Is(err, dataquery.ErrUnknownSource):
		return "unknown_source"
	case errors.Is(err, dataquery.ErrUnknownOp):
		return "unknown_operation"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "query_failed"
	}
}
