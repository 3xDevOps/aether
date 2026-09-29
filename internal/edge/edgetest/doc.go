// Package edgetest runs a real edge (the sign-in service and the relay on
// their two origins), real Aether servers (sshd with a real store and the
// edge agent) under each access policy, the real client dialer, the
// local gateway and a cookie-jar browser in one process, against a fake
// GitHub. A proxy in front of the edge can act as a compromised edge with
// the edge's own signing key. The tests check the security properties
// of edge remote access end to end; docs/edge.md describes the edge. The
// package holds only tests.
package edgetest
