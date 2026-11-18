package helper

import "testing"

func TestRAMEndSpansBothRAMRanges(t *testing.T) {
	iomem := `00000000-00000fff : Reserved
00001000-0009ffff : System RAM
000a0000-000fffff : Reserved
00100000-bfffffff : System RAM
  01000000-01ffffff : Kernel code
100000000-23fffffff : System RAM
`
	if end, err := ramEnd(iomem); err != nil || end != 0x240000000 {
		t.Fatalf("ramEnd = %#x, %v, want 0x240000000", end, err)
	}
	if _, err := ramEnd("00000000-00000fff : Reserved\n"); err == nil {
		t.Fatal("ramEnd found RAM in an iomem without any")
	}
}
