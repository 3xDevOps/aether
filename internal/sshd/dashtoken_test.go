package sshd

import (
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

// TestAllowMintBoundsAMember pins the dash-token mint limit: a member
// gets a burst, is then throttled, cannot starve another member, and is
// restored by the refill.
func TestAllowMintBoundsAMember(t *testing.T) {
	s := &Server{}
	now := time.Now()
	a, b := domain.MemberID("mem_a"), domain.MemberID("mem_b")
	for i := 0; i < mintBurst; i++ {
		if !s.allowMint(a, now) {
			t.Fatalf("mint %d refused inside the burst", i+1)
		}
	}
	if s.allowMint(a, now) {
		t.Fatal("mint allowed past the burst")
	}
	if !s.allowMint(b, now) {
		t.Fatal("another member throttled by the first member's bucket")
	}
	if !s.allowMint(a, now.Add(mintRefill)) {
		t.Fatal("refill did not restore a mint")
	}
	if s.allowMint(a, now.Add(mintRefill)) {
		t.Fatal("refill restored more than one mint")
	}
}
