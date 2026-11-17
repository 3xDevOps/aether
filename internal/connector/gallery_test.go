package connector

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

type consentState struct {
	name string
	page consentPage
}

// consentStates is the consent page in every state a person can meet.
func consentStates() []consentState {
	base := consentPage{Client: "ChatGPT (chatgpt.com)", Server: "moss", ServerID: "q3k7m2xw4vbn6hzt5rjc8pdyfa", Request: "fixture"}
	code := base
	codeErr := base
	codeErr.Error = "That code is wrong, expired or already used. Get a new one from Settings → ChatGPT and Claude in the Cloudfield app, or aether mcp, and paste it here."
	codeErr.Write = true
	member := base
	member.Universal, member.Member, member.Account, member.Write = true, "octo-dev", "octo-dev", true
	long := member
	long.Client = "Claude (claude.ai)"
	long.Server = "team-build-server-in-the-frankfurt-region-number-two"
	long.Member, long.Account = "a.very.long.address.for.a.person@subdomain.example.com", "a.very.long.address.for.a.person@subdomain.example.com"
	long.Control = true
	return []consentState{
		{"consent-code", code},
		{"consent-code-error", codeErr},
		{"consent-member", member},
		{"consent-long", long},
		{"consent-failed", consentPage{Failed: true, Server: "moss", Error: "This form was not issued by this server. Start the connection again from ChatGPT or Claude."}},
	}
}

// loadsFromAnotherHost finds a URL that would make a browser fetch from
// another host.
var loadsFromAnotherHost = regexp.MustCompile(`(?i)(?:\bsrc\s*=\s*["']?|<link[^>]*\bhref\s*=\s*["']?|url\(\s*["']?|@import\s+["']?)(?:[a-z][a-z0-9+.-]*:|//)`)

func TestConsentPageRendersEveryState(t *testing.T) {
	for _, st := range consentStates() {
		t.Run(st.name, func(t *testing.T) {
			var b bytes.Buffer
			if err := consentTemplate.Execute(&b, st.page); err != nil {
				t.Fatal(err)
			}
			body := b.String()
			if n := strings.Count(body, "<h1"); n != 1 {
				t.Errorf("%d h1 elements, want 1", n)
			}
			if m := loadsFromAnotherHost.FindString(body); m != "" {
				t.Errorf("page loads from another host: %q", m)
			}
			if st.page.Error != "" && !strings.Contains(body, st.page.Error) {
				t.Errorf("page does not show the error %q", st.page.Error)
			}
		})
	}
}

// TestConsentGallery serves every consent state at CONSENT_GALLERY_ADDR,
// under the sandboxing policy the edge's relay gives every page a server
// serves through it, until the test is stopped:
//
//	CONSENT_GALLERY_ADDR=127.0.0.1:8791 go test -run TestConsentGallery -timeout 0 ./internal/connector
func TestConsentGallery(t *testing.T) {
	addr := os.Getenv("CONSENT_GALLERY_ADDR")
	if addr == "" {
		t.Skip("CONSENT_GALLERY_ADDR is not set")
	}
	const relayCSP = "sandbox allow-forms; default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'"
	states := consentStates()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString("<!doctype html><title>Consent pages</title><ul>")
		for _, st := range states {
			fmt.Fprintf(&b, `<li><a href="/g/%s">%s</a>`, st.name, st.name)
		}
		w.Write([]byte(b.String())) //nolint:errcheck // a test page
	})
	for _, st := range states {
		mux.HandleFunc("GET /g/"+st.name, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", relayCSP)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := consentTemplate.Execute(w, st.page); err != nil {
				t.Error(err)
			}
		})
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("serving %d consent states at http://%s/", len(states), ln.Addr())
	t.Fatal(http.Serve(ln, mux))
}
