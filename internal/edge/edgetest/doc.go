// Package edgetest runs a real edge (the sign-in service and the relay, in
// development mode), real Aether servers (sshd with a real store and the
// edge agent) and the real client dialer in one process, against a fake
// GitHub, and checks the security properties of edge remote access end to
// end. docs/edge.md describes the edge. The package holds only tests.
package edgetest
