package observability

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	got := Redact(`{"api_key":"secret-123","client_secret":"client-456","password":"pass","text":"safe","auth":"Bearer token-456"} url?access_token=query-789&x=1`)
	for _, secret := range []string{"secret-123", "client-456", `"pass"`, "token-456", "query-789"} {
		if strings.Contains(got, secret) {
			t.Fatalf("secret %q leaked: %s", secret, got)
		}
	}
	if !strings.Contains(got, "safe") {
		t.Fatalf("non-secret content removed: %s", got)
	}
}
