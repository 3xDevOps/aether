# GitHub-only sign-in and an edge container image

**Status:** Implemented in PR #PR_NUMBER_PLACEHOLDER. The sections below
describe what was built; [edge.md](../edge.md) is the operator and user
guide, and [Implementation notes](#5-implementation-notes) lists where the
build refined this plan.

**Goal:** The edge signs people in with GitHub only, ships as an official
container image built by the release, and its image checks run in CI only
when a change can affect it.

An **edge** is the sign-in service and relay that Aether servers and clients
dial out to; [edge.md](../edge.md) is its guide. This plan changes the edge
that [Edge access policies](2026-09-28-edge-access-policies.md) describes and
keeps every security property of that plan.

## 1. Decisions

1. Google sign-in is removed: provider code, routes, configuration, secret
   handling, pages, client and dashboard choices, tests and documentation.
   Nothing is hidden behind a switch.
2. An identity stays `(provider, subject)` with `github` as the only
   provider. The server's schema and the wire contract shipped in
   `v0.5.2-alpha.3` with that pair, and a shipped migration is never edited.
   What goes is the machinery for choosing between providers: flags,
   parameters, registries and branches that only a second provider needed.
3. The edge's internal account id stays distinct from the GitHub identity.
4. No migration for Google accounts. No edge was deployed with Google
   sign-in. An edge or server that holds a Google identity refuses it with
   the reason and the command that removes it.
5. The image is `ghcr.io/3xdevops/aether-edge`, beside the project's other
   images. It holds the `aether-edge` binary and CA certificates on a
   minimal base, runs as a non-root user, and carries no configuration.
6. A proxy in another container is not on loopback. The edge accepts a
   forwarded client address from peers in networks the operator names, and
   from nobody else.
7. The release builds the image for `linux/amd64` and `linux/arm64` and
   tags it like the project's other images. A release never skips it.
8. CI builds and exercises the image when a changed path is in the edge's
   dependency surface, which a test derives from `go list` so the filter
   cannot fall behind the code.

## 2. Properties to keep

Verified by the tests in `internal/edge/edgetest` and `internal/sshd`, run
again on this change:

- The edge admits; the server authorizes.
- A compromised edge cannot create an approved device.
- A member's first device waits for approval under `approved-devices`.
- A claim binds to the account the client signed in as.
- A claim secret is sent only after the host key matches the server id.
- The access policy changes only on the server's machine.
- Switching to `approved-devices` approves nothing.
- Tailnet identity and SSH keys do not depend on the edge.

## 3. State and secrets in a container

| Item | Requirement |
| --- | --- |
| Data directory | One configurable path holding the database, the edge signing key and, with the edge's own certificates, the ACME cache. It must outlive the container |
| Edge signing key | Servers pin it. Losing it makes every server refuse the edge until its operator trusts the new key |
| GitHub client secret | Read from a file named by configuration, or from the environment. Never a build argument, never in the image |

## 4. Testing

- Go tests with the race detector, including the malicious-edge tests.
- Container: builds for both architectures; runs as non-root; refuses a
  missing or partial configuration with the reason; serves health; stops on
  SIGTERM within its grace period; keeps its signing key across a
  replacement that keeps the data directory, and creates a new one without
  it.
- Change detection: a fixture per kind of path, and a test that every
  package `aether-edge` imports is covered.

## 5. Implementation notes

- **Proxy trust.** `--trusted-proxies` (`AETHER_EDGE_TRUSTED_PROXIES`)
  names the proxies' networks and requires `--proxy-listen`, which may then
  be any address. With it, loopback is no longer trusted implicitly, the
  client address is the right-most `X-Forwarded-For` entry outside the
  networks, and a list that trusts every address is refused. Without it,
  behaviour is unchanged.
- **Health.** `aether-edge healthcheck` asks for `/healthz` on the
  loopback metrics listener, which answers only once the public listener
  is bound. It needs no certificate, host name or forwarded address in any
  listen mode.
- **Data directory.** The edge creates it with mode 0700 and refuses one
  that another uid owns or that it cannot write, naming the command that
  fixes it. The database keeps SQLite's temporary storage in memory, so the
  root filesystem may be read-only.
- **Partial configuration.** The image's smoke test covers a start with no
  configuration; the refusals of a partial one, such as a client id
  without its secret or an unreadable secret file, are covered by the
  binary's tests in `cmd/aether-edge`.
- **Pinning.** The Dockerfile pins both base images by digest, and the
  release's edge job pins its actions by commit.
- **Google data from v0.5.2-alpha.3.** Accounts and identities are listed
  and removable and admit nobody. A v0.5.2-alpha.3 server that still holds
  Google identities has its directory refused by a new edge, which closes
  its control connection; such a server is upgraded with the edge.
