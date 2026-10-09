package execution

import "testing"

func TestPhase6EventsAreKnownAndPersistable(t *testing.T) {
	for _, kind := range []Type{IntentClassified, DataQueryStarted, DataQueryCompleted, DataQueryFailed, AttachmentProcessed, ModelFallback} {
		if !kind.Known() || !kind.Execution() {
			t.Errorf("phase-6 event %q is not a known execution event", kind)
		}
	}
	if IntentClassified.Terminal() || AttachmentProcessed.Terminal() {
		t.Fatal("phase-6 progress event was incorrectly marked terminal")
	}
}
