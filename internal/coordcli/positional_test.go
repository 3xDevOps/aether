package coordcli

import (
	"context"
	"strings"
	"testing"
)

func TestPositionalBody(t *testing.T) {
	for _, tc := range []struct {
		name               string
		args               []string
		target, body       string
		wantTarget, wantBd string
		wantErr            string
	}{
		{name: "target and body positional", args: []string{"run-1", "hello", "there"}, wantTarget: "run-1", wantBd: "hello there"},
		{name: "target flag, body positional", args: []string{"hello"}, target: "run-1", wantTarget: "run-1", wantBd: "hello"},
		{name: "target flag, words", args: []string{"hello", "there"}, target: "run-1", wantTarget: "run-1", wantBd: "hello there"},
		{name: "body flag wins", args: []string{"run-1"}, body: "flagged", wantTarget: "run-1", wantBd: "flagged"},
		{name: "trailing flag after body", args: []string{"hello", "--json"}, target: "run-1", wantErr: "flag --json after the message body; put flags before the body"},
		{name: "trailing flag after target and body", args: []string{"run-1", "hello", "--json"}, wantErr: "flag --json after the message body"},
		{name: "flag with a value after the body", args: []string{"hello", "--idempotency-key", "retry-1"}, target: "run-1", wantErr: "flag --idempotency-key after the message body"},
		{name: "stdin dash is a body", args: []string{"-"}, target: "run-1", wantTarget: "run-1", wantBd: "-"},
		{name: "no body", args: []string{}, target: "run-1", wantTarget: "run-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, body := tc.target, tc.body
			err := positionalBody(tc.args, &target, &body)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || target != tc.wantTarget || body != tc.wantBd {
				t.Fatalf("target %q body %q err %v; want %q %q", target, body, err, tc.wantTarget, tc.wantBd)
			}
		})
	}
}

func TestSendRefusesAnEmptyBody(t *testing.T) {
	_, err := send(context.Background(), "unused.sock", []string{"--to", "run-1"}, strings.NewReader(""), nil)
	if err == nil || !strings.Contains(err.Error(), "a message body is required") {
		t.Fatalf("send with no body = %v", err)
	}
}
