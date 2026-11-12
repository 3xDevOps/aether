package main

import (
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestMemberNamesReturnsAFailedMemberList(t *testing.T) {
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodMemberList: {err: &protocol.Error{Code: protocol.CodeInternal, Message: "store: boom"}},
	})
	names, err := memberNames(c)
	if err == nil || !strings.Contains(err.Error(), "list members") || !strings.Contains(err.Error(), "store: boom") {
		t.Fatalf("memberNames = %v, %v, want the member.list error with context", names, err)
	}
}

func TestMemberNamesMapsIDsToDisplayNames(t *testing.T) {
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodMemberList: {result: protocol.MemberListResult{Members: []protocol.Member{{ID: "m1", DisplayName: "Ada"}}}},
	})
	names, err := memberNames(c)
	if err != nil || names["m1"] != "Ada" {
		t.Fatalf("memberNames = %v, %v", names, err)
	}
}
