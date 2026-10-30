package execution

// RecoveryState is derived from the append-only event stream. It deliberately
// contains identifiers and state only; prompt, file and knowledge bodies are
// never copied into the recovery decision.
type RecoveryState struct {
	RunID             string
	TurnID            string
	OpenStepID        string
	OpenAttemptID     string
	PendingToolCallID string
	PendingApproval   bool
	Terminal          bool
	StopReason        string
	SafeToRetry       bool
}

// InspectRecovery derives the last durable execution point. A model request
// without a matching model_completed event, or a tool_started without a
// terminal tool event, is considered interrupted and may be retried only when
// no approval is pending.
func InspectRecovery(events []Event) RecoveryState {
	state := RecoveryState{}
	for _, ev := range events {
		if state.RunID == "" {
			state.RunID = ev.RunID
		}
		if ev.TurnID != "" {
			state.TurnID = ev.TurnID
		}
		if ev.Type.Terminal() {
			state.Terminal = true
			if reason, ok := ev.Payload["stop_reason"].(string); ok {
				state.StopReason = reason
			}
			continue
		}
		switch ev.Type {
		case PromptAssembled, ModelWaiting, ModelRequest:
			// A fresh model request after an approval event means the checkpoint
			// has resumed; the old approval is no longer pending.
			state.PendingApproval = false
			if ev.StepID != "" {
				state.OpenStepID = ev.StepID
			}
			if ev.AttemptID != "" {
				state.OpenAttemptID = ev.AttemptID
			}
		case ModelCompleted:
			if ev.AttemptID == state.OpenAttemptID {
				state.OpenAttemptID = ""
			}
		case ToolStarted:
			state.PendingToolCallID, state.OpenStepID, state.OpenAttemptID = payloadString(ev, "tool_call_id"), ev.StepID, ev.AttemptID
		case ToolCompleted, ToolFailed, SkillLoaded, FileReadDone:
			if callID, ok := ev.Payload["tool_call_id"].(string); ok && callID == state.PendingToolCallID {
				state.PendingToolCallID = ""
			}
		case ApprovalRequired:
			state.PendingApproval = true
		}
	}
	state.SafeToRetry = !state.Terminal && !state.PendingApproval && (state.OpenAttemptID != "" || state.PendingToolCallID != "")
	if state.PendingApproval {
		state.SafeToRetry = false
	}
	return state
}

func payloadString(ev Event, key string) string {
	if ev.Payload == nil {
		return ""
	}
	value, _ := ev.Payload[key].(string)
	return value
}
