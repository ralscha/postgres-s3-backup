package app

import (
	"context"
	"errors"
	"testing"
)

func TestResultAfterCancellation(t *testing.T) {
	expectedErr := errors.New("operation failed")
	if got := resultAfterCancellation(context.Background(), expectedErr); !errors.Is(got, expectedErr) {
		t.Fatalf("resultAfterCancellation() = %v, want %v", got, expectedErr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := resultAfterCancellation(ctx, expectedErr); got != nil {
		t.Fatalf("resultAfterCancellation() = %v, want nil after cancellation", got)
	}
}
