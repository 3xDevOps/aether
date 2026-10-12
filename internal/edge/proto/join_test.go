package edgeproto

import (
	"strings"
	"testing"
)

func TestJoinLink(t *testing.T) {
	const id = "wqc4lsjvabcdefghijklmnopqr"
	secret := NewJoinSecret()
	if !validJoinSecret(secret) || secret == NewJoinSecret() {
		t.Fatalf("NewJoinSecret = %q", secret)
	}
	link := JoinLink(testOrigin, id, "inv-7k2m9q4xbd", secret)
	if link != testOrigin+"/join/"+id+"/inv-7k2m9q4xbd#"+secret {
		t.Fatalf("JoinLink = %q", link)
	}
	for _, in := range []string{
		link,
		"  " + strings.Replace(link, "edge.example", "Edge.Example", 1) + "\n",
		"aether://join?edge=" + testOrigin + "&server=" + id + "&invitation=inv-7k2m9q4xbd#" + secret,
		"aether://join?edge=https%3A%2F%2Fedge.example&invitation=inv-7k2m9q4xbd&server=" + id + "#" + secret,
	} {
		edge, gotID, inv, gotSecret, err := ParseJoinLink(in)
		if err != nil || edge != testOrigin || gotID != id || inv != "inv-7k2m9q4xbd" || gotSecret != secret {
			t.Errorf("ParseJoinLink(%q) = %q, %q, %q, %q, %v", in, edge, gotID, inv, gotSecret, err)
		}
	}
	for _, bad := range []string{
		"",
		testOrigin + "/join/" + id + "/inv-7k2m9q4xbd",
		testOrigin + "/join/" + id + "/inv-7k2m9q4xbd#" + secret[:15],
		testOrigin + "/join/" + id + "/inv-7k2m9q4xbd#" + strings.ToUpper(secret),
		testOrigin + "/join/" + id + "/inv-7k2m9q4xbd/#" + secret,
		testOrigin + "/join/" + id[:25] + "/inv-7k2m9q4xbd#" + secret,
		testOrigin + "/join/" + id + "/inv:x#" + secret,
		testOrigin + "/claim/" + id + "/inv-7k2m9q4xbd#" + secret,
		testOrigin + "/join/" + id + "/inv-7k2m9q4xbd?x=1#" + secret,
		"http://edge.example/join/" + id + "/inv-7k2m9q4xbd#" + secret,
		"https://user@edge.example/join/" + id + "/inv-7k2m9q4xbd#" + secret,
		"aether://claim?edge=" + testOrigin + "&server=" + id + "&invitation=inv-7k2m9q4xbd#" + secret,
		"aether://join?edge=" + testOrigin + "&server=" + id + "#" + secret,
		"aether://join?edge=" + testOrigin + "&server=" + id + "&invitation=inv-7k2m9q4xbd&x=1#" + secret,
		"aether://join?edge=edge.example&server=" + id + "&invitation=inv-7k2m9q4xbd#" + secret,
	} {
		if _, _, _, _, err := ParseJoinLink(bad); err == nil {
			t.Errorf("ParseJoinLink(%q) accepted", bad)
		} else if strings.Contains(err.Error(), secret) {
			t.Errorf("ParseJoinLink(%q) error carries the secret: %v", bad, err)
		}
	}
}

func TestClaimLink(t *testing.T) {
	id := ServerID(seedSigner(t, 1).PublicKey())
	code, err := NewClaimCode(id)
	if err != nil {
		t.Fatal(err)
	}
	link, err := ClaimLink(testOrigin+"/", strings.ToUpper(code))
	if want := "aether://claim?edge=https%3A%2F%2Fedge.example&server=" + id + "#" + code[len(id)+1:]; err != nil || link != want {
		t.Fatalf("ClaimLink = %q, %v; want %q", link, err, want)
	}
	if _, err := ClaimLink(testOrigin, code[:len(code)-1]); err == nil {
		t.Error("ClaimLink accepted a short code")
	}
	if _, err := ClaimLink("edge.example", code); err == nil {
		t.Error("ClaimLink accepted an edge url without a scheme")
	}
}

func TestJoinUser(t *testing.T) {
	secret := NewJoinSecret()
	owner := Principal{Type: PrincipalAccount, Provider: ProviderGitHub, Subject: "1001"}
	user, err := JoinUser("inv-7k2m9q4xbd", secret, owner)
	if err != nil || user != "join:inv-7k2m9q4xbd:"+secret+":github:1001" {
		t.Fatalf("JoinUser = %q, %v", user, err)
	}
	if inv, got, gotOwner, ok := ParseJoinUser(user); !ok || inv != "inv-7k2m9q4xbd" || got != secret || gotOwner != owner {
		t.Fatalf("ParseJoinUser(%q) = %q, %q, %+v, %v", user, inv, got, gotOwner, ok)
	}
	if _, err := JoinUser("inv-7k2m9q4xbd", "short", owner); err == nil {
		t.Fatal("JoinUser accepted a malformed secret")
	}
	if _, err := JoinUser("inv-7k2m9q4xbd", secret, Principal{Type: PrincipalTeam, Provider: ProviderGitHub, Subject: "1"}); err == nil {
		t.Fatal("JoinUser accepted a team")
	}
	for _, bad := range []string{
		"",
		"join:",
		"github:1001",
		"join:inv-7k2m9q4xbd:" + secret,
		"join:inv-7k2m9q4xbd:" + secret + ":github:",
		"join:inv-7k2m9q4xbd:" + secret + ":gitlab:1001",
		"join::" + secret + ":github:1001",
		"join:inv-7k2m9q4xbd:" + secret[:15] + ":github:1001",
		"join:INV-7k2m9q4xbd:" + secret + ":github:1001",
		"claim:inv-7k2m9q4xbd:" + secret + ":github:1001",
		"join:inv-7k2m9q4xbd:" + secret + ":github:10\x0001",
	} {
		if _, _, _, ok := ParseJoinUser(bad); ok {
			t.Errorf("ParseJoinUser(%q) accepted", bad)
		}
	}
}
