package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// discoveredKeys are the private-key files tried, in order, when no key
// is configured. They mirror what ssh itself offers so a user whose key
// is not the ed25519 default needs no configuration.
var discoveredKeys = []string{"id_ed25519", "id_ecdsa", "id_rsa"}

// Auth is the client authentication resolved for one connection: the
// methods to offer plus a note on every candidate examined, so a failed
// handshake can say what was tried and how to fix it. Nothing is printed
// while resolving; a server that accepts the connection without a key
// (a tailnet identity) never hears about an unusable local key.
type Auth struct {
	methods []ssh.AuthMethod
	closeFn func()
	tried   []string
	// explicit is set when cfg.Key chose the key, so the advice can
	// mention the way back to automatic discovery.
	explicit bool
	// banners collects what the server said during the handshake. The
	// client otherwise drops them, and "no Aether member for this key"
	// is the one line that explains most rejected keys.
	banners []string
}

// ResolveAuth examines the SSH agent and the key files cfg selects and
// returns the auth methods to offer. It never fails: an unusable key is
// recorded and surfaces through Explain or Missing, so a server that
// needs no key still connects.
//
// With cfg.Key set the choice is deterministic: only that key is offered.
// A passphrase-protected file is still usable when the agent holds the
// same key, matched by public key; any other agent key is ignored so an
// explicit choice never silently authenticates as a different identity.
// Without cfg.Key every agent key is offered, then each discoveredKeys
// file under ~/.ssh that parses.
func ResolveAuth(cfg Config) *Auth {
	a := &Auth{}
	fromAgent, closeAgent, agentNote := agentSigners()
	a.closeFn = closeAgent
	var signers []ssh.Signer
	if cfg.Key != "" {
		a.explicit = true
		signers = a.explicitKey(cfg.Key, fromAgent)
	} else {
		a.tried = append(a.tried, "ssh-agent: "+agentNote)
		signers = append(signers, fromAgent...)
		for _, name := range discoveredKeys {
			path := defaultPath(".ssh", name)
			if path == "" {
				continue
			}
			signers = append(signers, a.discoveredKey(path, signers)...)
		}
	}
	if len(signers) > 0 {
		a.methods = []ssh.AuthMethod{ssh.PublicKeys(signers...)}
	}
	return a
}

// Methods are the auth methods to hand ssh.ClientConfig; empty means the
// connection can only succeed if the server needs no key.
func (a *Auth) Methods() []ssh.AuthMethod { return a.methods }

// Offered reports whether at least one signer was found.
func (a *Auth) Offered() bool { return len(a.methods) > 0 }

// Close releases the agent connection once the handshake is over.
func (a *Auth) Close() {
	if a.closeFn != nil {
		a.closeFn()
	}
}

// Banner is the ssh.BannerCallback that records server messages for
// Explain instead of printing them.
func (a *Auth) Banner(message string) error {
	msg := strings.Join(strings.Fields(message), " ")
	if msg != "" {
		a.banners = append(a.banners, msg)
	}
	return nil
}

// Explain turns a handshake error into an actionable one. Authentication
// failures gain the list of candidates examined and the ways to recover:
// ssh-add for a locked key, --key to pick a file, --key auto to drop an
// explicit choice. Any other error - a host-key mismatch, a refused
// connection - is returned as is.
func (a *Auth) Explain(err error) error {
	if err == nil || !isAuthFailure(err) {
		return err
	}
	head := err.Error() + "\nthe server wants an SSH key"
	if a.Offered() {
		head += " it recognizes"
	}
	return a.report(head)
}

// Missing is the error for a connection that must authenticate when no
// key could be offered at all.
func (a *Auth) Missing() error {
	return a.report("cli: no usable SSH key")
}

func (a *Auth) report(head string) error {
	var b strings.Builder
	b.WriteString(head)
	b.WriteString("; tried:")
	for _, line := range a.tried {
		b.WriteString("\n  ")
		b.WriteString(line)
	}
	for _, msg := range a.banners {
		b.WriteString("\n  server said: ")
		b.WriteString(msg)
	}
	if a.explicit {
		b.WriteString("\nunlock it with ssh-add <private-key>, choose another with --key <private-key>, or return to automatic discovery with aether link <addr> --key auto")
	} else {
		b.WriteString("\nunlock a key with ssh-add <private-key>, or choose one with --key <private-key>")
	}
	return errors.New(b.String())
}

// isAuthFailure matches the error x/crypto/ssh returns when every offered
// method was refused; it carries no typed error, only this text.
func isAuthFailure(err error) bool {
	return strings.Contains(err.Error(), "unable to authenticate")
}

// explicitKey loads the one key the user chose. Agent keys count only when
// they are that key: a passphrase-protected file whose public half the
// agent holds is offered through the agent, nothing else is.
func (a *Auth) explicitKey(path string, fromAgent []ssh.Signer) []ssh.Signer {
	k := readKey(path)
	switch k.state {
	case keyUsable:
		a.tried = append(a.tried, path+": offered")
		return []ssh.Signer{k.signer}
	case keyLocked:
		if s := findSigner(fromAgent, k.pub); s != nil {
			a.tried = append(a.tried, path+": offered (unlocked by ssh-agent)")
			return []ssh.Signer{s}
		}
	}
	a.tried = append(a.tried, path+": "+k.note)
	return nil
}

// discoveredKey loads one default key file, skipping keys the agent
// already offers so the same identity is not presented twice (each
// refused offer counts toward the server's attempt limit).
func (a *Auth) discoveredKey(path string, have []ssh.Signer) []ssh.Signer {
	k := readKey(path)
	if findSigner(have, k.pub) != nil {
		a.tried = append(a.tried, path+": offered (via ssh-agent)")
		return nil
	}
	a.tried = append(a.tried, path+": "+k.note)
	if k.state != keyUsable {
		return nil
	}
	return []ssh.Signer{k.signer}
}

// CheckKey validates a key file the user chose explicitly, before anything
// is saved or dialed. A passphrase-protected key passes: the agent may
// unlock it at connect time.
func CheckKey(path string) error {
	k := readKey(path)
	switch k.state {
	case keyUsable, keyLocked:
		return nil
	case keyMissing:
		return fmt.Errorf("cli: ssh key %s: not found", path)
	default:
		return fmt.Errorf("cli: ssh key %s: %s", path, k.note)
	}
}

type keyState int

const (
	keyUsable keyState = iota
	keyLocked
	keyMissing
	keyPublic
	keyUnreadable
)

// keyFile is the outcome of reading one private-key file. signer is set
// for a usable key; pub is set whenever the public half is known, which
// for a locked key lets an agent copy be matched. note says what happened
// in words a new user can act on.
type keyFile struct {
	state  keyState
	signer ssh.Signer
	pub    ssh.PublicKey
	note   string
}

func readKey(path string) keyFile {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return keyFile{state: keyMissing, note: "not found"}
		}
		return keyFile{state: keyUnreadable, note: fmt.Sprintf("read ssh key: %v", err)}
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err == nil {
		return keyFile{state: keyUsable, signer: signer, pub: signer.PublicKey(), note: "offered"}
	}
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		pub := missing.PublicKey
		if pub == nil {
			pub = siblingPublicKey(path)
		}
		return keyFile{state: keyLocked, pub: pub, note: "passphrase protected; unlock it with: ssh-add " + path}
	}
	if pub, _, _, _, perr := ssh.ParseAuthorizedKey(raw); perr == nil && pub != nil {
		return keyFile{state: keyPublic, note: "is a public key; --key takes the private key (usually the same path without .pub)"}
	}
	return keyFile{state: keyUnreadable, note: fmt.Sprintf("parse ssh key %s: %v", path, err)}
}

// siblingPublicKey reads <path>.pub, which ssh-keygen writes next to every
// key, for locked keys whose encrypted format carries no public half.
func siblingPublicKey(path string) ssh.PublicKey {
	raw, err := os.ReadFile(path + ".pub")
	if err != nil {
		return nil
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(raw)
	if err != nil {
		return nil
	}
	return pub
}

// findSigner returns the signer in signers for pub, or nil.
func findSigner(signers []ssh.Signer, pub ssh.PublicKey) ssh.Signer {
	if pub == nil {
		return nil
	}
	want := string(pub.Marshal())
	for _, s := range signers {
		if string(s.PublicKey().Marshal()) == want {
			return s
		}
	}
	return nil
}

// agentSigners collects the local SSH agent's signers. The note describes
// the outcome for Explain; a missing agent is not a failure, only a
// candidate that produced nothing.
func agentSigners() ([]ssh.Signer, func(), string) {
	conn, err := dialAgent(sshAgentTimeout)
	if err != nil {
		return nil, nil, fmt.Sprintf("connect ssh agent: %v", err)
	}
	if conn == nil {
		return nil, nil, "not running"
	}
	// The deadline guards every exchange with an agent that accepts the
	// connection and then never answers.
	if err = conn.SetDeadline(time.Now().Add(sshAgentTimeout)); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Sprintf("set ssh agent deadline: %v", err)
	}
	signers, err := agent.NewClient(conn).Signers()
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Sprintf("load ssh agent keys: %v", err)
	}
	if len(signers) == 0 {
		_ = conn.Close()
		return nil, nil, "running, but holds no keys (ssh-add <private-key>)"
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Sprintf("clear ssh agent deadline: %v", err)
	}
	return signers, func() { _ = conn.Close() }, fmt.Sprintf("offered %d key(s)", len(signers))
}
