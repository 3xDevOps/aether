package sshd

import (
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

func TestRunShellErrorsMapToWireCodes(t *testing.T) {
	t.Parallel()
	if got := rpcError(scheduler.ErrInvalidRunShellTab).Code; got != protocol.CodeInvalidParams {
		t.Fatalf("invalid tab code = %d, want %d", got, protocol.CodeInvalidParams)
	}
	if got := rpcError(scheduler.ErrRunShellTabLimit).Code; got != protocol.CodeConflict {
		t.Fatalf("tab limit code = %d, want %d", got, protocol.CodeConflict)
	}
}
