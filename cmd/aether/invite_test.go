package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestParseInviteArgs(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		want    *protocol.MemberInvitationCreateParams
		ttl     int
		wantErr string
	}{
		{args: nil, ttl: 86400},
		{args: []string{"--ttl", "3600"}, ttl: 3600},
		{
			args: []string{"--github", "octocat", "--role", "viewer"},
			want: &protocol.MemberInvitationCreateParams{Login: "octocat", Role: "viewer"},
		},
		{
			args: []string{"--email", "dana@example.com"},
			want: &protocol.MemberInvitationCreateParams{Email: "dana@example.com", Role: "collaborator"},
		},
		{
			args: []string{"--email", "dana@example.com", "--role", "admin"},
			want: &protocol.MemberInvitationCreateParams{Email: "dana@example.com", Role: "admin"},
		},
		{args: []string{"--github", "octocat", "--ttl", "60"}, wantErr: "--ttl applies to a one-time invite code"},
		{args: []string{"--role", "admin"}, wantErr: "--role applies to --github and --email"},
		{args: []string{"--github", "octocat", "extra"}, wantErr: "usage: aether invite"},
	} {
		got, ttl, err := parseInviteArgs(tc.args)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%q: err = %v, want %q", tc.args, err, tc.wantErr)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, tc.want) || ttl != tc.ttl {
			t.Errorf("%q = %+v, %d, %v; want %+v, %d", tc.args, got, ttl, err, tc.want, tc.ttl)
		}
	}
}
