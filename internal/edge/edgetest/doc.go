// Package edgetest runs a real edge (the sign-in service and the relay, in
// development mode, plus its SNI router), real Aether servers (sshd with a
// real store, the edge agent and the edge dashboard gateway), the real
// client dialer and a cookie-jar browser in one process, against a fake
// GitHub and a test CA, and checks the security properties of edge remote
// access end to end. docs/edge.md describes the edge. The package holds
// only tests.
package edgetest
