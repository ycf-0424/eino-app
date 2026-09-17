package model

import (
	"context"
	"errors"
	"testing"
)

func TestRetryEventuallySucceeds(t *testing.T) {
	attempts := 0
	err := Retry(context.Background(), 3, func() error {
		attempts++
		if attempts < 3 {
			return errors.New("connection refused")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("Retry() unexpected error: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("Retry() attempts = %d, want 3", attempts)
	}
}

func TestRetryStopsOnNonRetryableError(t *testing.T) {
	attempts := 0
	err := Retry(context.Background(), 3, func() error {
		attempts++
		return errors.New("invalid model name")
	})

	if err == nil {
		t.Fatal("Retry() expected an error")
	}
	if attempts != 1 {
		t.Fatalf("Retry() attempts = %d, want 1", attempts)
	}
}

func TestIsRetryableError(t *testing.T) {
	if !IsRetryableError(errors.New("connection refused")) {
		t.Fatal("connection refused should be retryable")
	}
	if IsRetryableError(context.Canceled) {
		t.Fatal("context cancellation should not be retryable")
	}
}
