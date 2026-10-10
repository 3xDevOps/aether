package devexec

import (
	"slices"
	"testing"
)

func TestLoopbackAddrsAdmitsOnlyLoopback(t *testing.T) {
	for host, want := range map[string][]string{
		"localhost":        {"127.0.0.1", "::1"},
		"127.0.0.1":        {"127.0.0.1"},
		"127.8.9.10":       {"127.8.9.10"},
		"::1":              {"::1"},
		"::ffff:127.0.0.1": {"::ffff:127.0.0.1"},
	} {
		got, err := LoopbackAddrs(host)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("LoopbackAddrs(%q) = %v %v, want %v", host, got, err, want)
		}
	}
	for _, host := range []string{
		"", "example.com", "localhost.example.com", "LOCALHOST", "0.0.0.0", "::", "10.0.0.1", "172.17.0.1",
		"169.254.169.254", "host.docker.internal", "::1%eth0", "127.0.0.1:80", "terminal", "run:run-0123456789",
	} {
		if got, err := LoopbackAddrs(host); err == nil {
			t.Errorf("LoopbackAddrs(%q) = %v, want it refused", host, got)
		}
	}
}
