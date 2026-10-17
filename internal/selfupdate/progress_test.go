package selfupdate

import (
	"strings"
	"testing"
	"time"
)

func TestProgressThrottlesAndFormats(t *testing.T) {
	const mb = 1 << 20
	clock := time.Unix(0, 0)
	var out strings.Builder
	p := newProgress(&out, 100*mb, 5*time.Second, func() time.Time { return clock })

	_, _ = p.Write(make([]byte, 10*mb))
	clock = clock.Add(4 * time.Second)
	_, _ = p.Write(make([]byte, 10*mb))
	if out.Len() != 0 {
		t.Fatalf("printed before the interval: %q", out.String())
	}
	clock = clock.Add(time.Second)
	_, _ = p.Write(make([]byte, 20*mb))
	clock = clock.Add(time.Second)
	_, _ = p.Write(make([]byte, 20*mb))
	if want := "  40 of 100 MB (40%)\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}

	out.Reset()
	unknown := newProgress(&out, 0, time.Second, func() time.Time { clock = clock.Add(time.Second); return clock })
	_, _ = unknown.Write(make([]byte, 3*mb))
	if want := "  3 MB\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}

	if n, err := NewProgress(nil, 1).Write([]byte("x")); n != 1 || err != nil {
		t.Fatalf("nil writer: %d, %v", n, err)
	}
}
