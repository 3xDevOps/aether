package main

import (
	"bufio"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

var testMergePlan = protocol.MemberMergePlan{
	Minted:   protocol.MergeMember{ID: "m-admin", DisplayName: "Ada", Role: "admin", TailnetLogin: "ada@"},
	Proposer: protocol.MergeMember{ID: "m-gh", DisplayName: "Ada GH", Role: "collaborator"},
	Role:     "collaborator", RoleWithKeepAdmin: "admin", KeepAdminRequired: true,
	SignIns:    []string{"tailnet login ada@", "github account ada at the edge"},
	PlanDigest: "d",
	Choices: []protocol.MergeChoice{
		{KeepSettingsOf: "m-admin", AbsorbedID: "m-gh", Discarded: protocol.MergeSettings{DisplayName: "Ada GH", Image: "img:1"}},
		{KeepSettingsOf: "m-gh", AbsorbedID: "m-admin", Discarded: protocol.MergeSettings{DisplayName: "Ada"}},
	},
}

func TestMergeProposal(t *testing.T) {
	for _, tc := range []struct {
		name, input, keep string
		yes               bool
		want              protocol.MemberMergeProposeParams
		wantErr           string
		wantOut           []string
	}{
		{name: "asks whose settings and to discard", input: "1\ny\n",
			want:    protocol.MemberMergeProposeParams{Code: "C", KeepSettingsOf: "m-admin", PlanDigest: "d", DiscardAck: true},
			wantOut: []string{"whose settings prevail, 1 or 2?", `display name "Ada GH"`, "saved environment image img:1", "or as admin if the admin side asks", "discard these settings and merge? [y/N]"}},
		{name: "declines the discard", input: "2\nn\n", wantErr: "nothing was proposed"},
		{name: "flags answer everything", keep: "m-gh", yes: true,
			want: protocol.MemberMergeProposeParams{Code: "C", KeepSettingsOf: "m-gh", PlanDigest: "d", DiscardAck: true}},
		{name: "keep names neither member", keep: "m-other", yes: true, wantErr: "neither"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			got, err := mergeProposal(testMergePlan, "C", tc.keep, false, tc.yes, true, bufio.NewReader(strings.NewReader(tc.input)), &out)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("params = %+v, %v; want %+v", got, err, tc.want)
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output lacks %q:\n%s", w, out.String())
				}
			}
		})
	}
}

// The admin who minted the code is asked separately whether to keep the
// merged member admin.
func TestAcceptDecisionAsksTheAdminSide(t *testing.T) {
	plan := testMergePlan
	p := protocol.MemberMergeProposal{ID: "mrp-1", MintedID: "m-admin", ProposerID: "m-gh", KeepSettingsOf: "m-admin", PlanDigest: "d", Plan: &plan}
	var out strings.Builder
	got, err := acceptDecision(p, false, false, true, bufio.NewReader(strings.NewReader("y\ny\n")), &out)
	if err != nil || got != (protocol.MemberMergeAcceptParams{ProposalID: "mrp-1", PlanDigest: "d", KeepAdmin: true}) {
		t.Fatalf("accept = %+v, %v", got, err)
	}
	for _, w := range []string{"member m-gh proposes merging with your member m-admin", "leaves the server without an admin", "keep it admin?", "accept this merge? [y/N]"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("output lacks %q:\n%s", w, out.String())
		}
	}
}

// Accepting without naming a proposal is refused while several are open,
// and without a terminal only after the plan is printed.
func TestAcceptNamesTheProposal(t *testing.T) {
	plan := testMergePlan
	open := []protocol.MemberMergeProposal{
		{ID: "mrp-1", MintedID: "m-admin", ProposerID: "m-gh", KeepSettingsOf: "m-admin", PlanDigest: "d", Plan: &plan},
		{ID: "mrp-2", MintedID: "m-admin", ProposerID: "m-mallory", KeepSettingsOf: "m-admin", PlanDigest: "e", Plan: &plan},
		{ID: "mrp-3", MintedID: "m-other", ProposerID: "m-admin", PlanDigest: "f", Plan: &plan},
	}
	if _, err := pickProposal(open, "m-admin", ""); err == nil || !strings.Contains(err.Error(), "2 proposals are open") {
		t.Fatalf("accept without an id = %v", err)
	}
	p, err := pickProposal(open, "m-admin", "mrp-2")
	if err != nil || p.ID != "mrp-2" {
		t.Fatalf("accept mrp-2 = %+v, %v", p, err)
	}
	if _, err = pickProposal(open, "m-admin", "mrp-3"); err == nil {
		t.Fatal("accepted a proposal made with another member's code")
	}
	var out strings.Builder
	_, err = acceptDecision(*p, false, false, false, bufio.NewReader(strings.NewReader("")), &out)
	if err == nil || !strings.Contains(err.Error(), "pass --yes") || !strings.Contains(out.String(), "proposes merging") {
		t.Fatalf("accept without a terminal = %v, output %q; want the plan printed, then a refusal", err, out.String())
	}
	got, err := acceptDecision(*p, false, true, false, bufio.NewReader(strings.NewReader("")), &out)
	if err != nil || got.PlanDigest != plan.PlanDigest {
		t.Fatalf("accept --yes = %+v, %v; want the printed plan's digest", got, err)
	}
}
