# Rename to Cloudfield and Move to cloudfield.ai

**Goal:** the product is named Cloudfield everywhere a person reads or types
its name, and lives at `cloudfield.ai`. An install that has not updated yet
keeps running, keeps reaching its edge, and still finds its update.

**Status:** approved by the owner, 2026-11-16. The rename and the new
domain are the owner's decisions of 2026-11-15; the choices under
[Decisions](#decisions-2026-11-16) are the owner's of 2026-11-16. Phases 0,
1 and 5 and the code of phase 2 are merged. Phase 2 waits on the owner's
switch steps, phase 3 on item 20 (#1025) and phase 4 on item 23 (#1026).

## The rule

A name changes when a person reads or types it. A name only programs read
stays `aether`, because renaming it means carrying both names in code for
no visible gain.

A changed name that anything already installed depends on gets the new name
**beside** the old one. The old name keeps working with no end date unless
this plan gives one. Three things can never be renamed safely, because an
old updater cannot be told about the change: the signed manifest
`aether-release.json`, the release file names, and the names an old binary
uses after a rollback.

Terms used below. A **shim** is a change that makes the software accept a
new name before anything uses it. The server's **update trial** starts the
new binary and, if it fails its checks, puts the old binary and database
back (`internal/serverupdate/trial.go`). An **address pair** is the two
addresses an edge serves: one for sign-in pages, one for the relay.

## Inventory

Counted on `main` at `57ac19ecb`. 2,107 of 3,364 files contain `aether` in
any case, on 16,924 lines. About 3,990 of those lines are Go import paths,
about 7,000 are tests, and 421 are in `docs/plans/`.

### Shown to people

| Area | Count | Examples |
| --- | --- | --- |
| Dashboard copy | 134 lines, 55 files | `web/src/app/layout.tsx:14-18` (title, home-screen name), `web/src/app/manifest.ts:18-20`, `web/public/sw.js:40` (notification title) |
| CLI, server and edge messages | 340 lines, 162 files | `cmd/aether/agent_login.go:40`, `cmd/aether/answer.go:108`; 692 error strings mention the name or a command |
| Edge web pages | 15 lines, 9 templates | `internal/edge/service/templates/join.html:2-6` |
| Text agents read | about 120 lines | `internal/assistant/instructions.md:1-9`, `internal/coordcli/cli.go`, `internal/coordhooks/guide.go`, `internal/connector/connector.go:62-65` |
| Docs | 463 lines, 39 files | `docs/security.md` (99), `docs/privacy.md` (44), `docs/legal/` |
| Root files | 5 files, 138 lines | `README.md:5`, `CONTRIBUTING.md`, `AGENTS.md`, `CLAUDE.md`, `AI_POLICY.md` |
| Old plans | 421 lines, 24 files | `docs/plans/`; dated records, left as written |
| Desktop app | 60 lines, 10 files | `desktop/electron-builder.yml:9` (`productName`), `desktop/main.js:141`, `packaging/windows/versioninfo.json:12-14` |
| Android app | 215 lines, 48 files | `android/app/src/main/res/values/strings.xml:3` (`app_name`), `android/listing/` (Play listing, unpublished) |
| Connector listings | 2 directories | `packaging/claude-connector/listing.json`, `packaging/chatgpt-plugin/plugin.json` |
| Logos and icons | about 25 files | `web/public/aether-mark.png`, `web/public/icons/`, `desktop/build/`, Android `mipmap-*`, `docs/assets/aether-banner.svg` |
| Tests asserting this copy | about 480 lines | 218 Go, 116 dashboard unit, 86 end-to-end, 59 Android; `scripts/update-smoke.ps1:408` |

Sub-product names in copy today: "Aether Edge" (10 lines in docs, Android
`choice_edge`), "Aether relay", "Aether server", "Aether dashboard",
"Aether Desktop", "Aether Android", "Aether CLI", "Aether data", "Aether
Coding Agents" (ChatGPT listing), "Ask Aether". "Enhanced", "Standard" and
"Assistant" never carry the product name.

### Typed or scripted

| Name | Count | Defined at |
| --- | --- | --- |
| Commands `aether`, `aether-server`, `aether-edge` | 3 binaries | `cmd/aether`, `cmd/aether-server`, `cmd/aether-edge` |
| `aether-internal` | 1 alias, 275 lines | the server binary run under that name (`cmd/aether-server/main.go:57`), mounted at `/usr/local/bin/aether-internal` in every session (`internal/coordtransport/paths.go:25`) |
| `AETHER_*` variables | 145 distinct, about 55 test-only | installer `scripts/install.sh:29-48`; CLI `internal/cli/config.go:204`; set inside sessions `internal/coordtransport/paths.go:33-49`; edge `cmd/aether-edge/options.go:63-93` |
| MCP server name and tools | `aether` and 55 `aether_*` tools | `internal/mcpbridge/mcpbridge.go:28`, `internal/mcpbridge/tools.go:20-25`, `internal/ops/` |
| systemd units | 6 | `packaging/systemd/aether-server.service`, `aether.slice`, four `aether-edge*` units |
| Sync daemon service | 3 names | systemd user unit and Windows scheduled task `aether-daemon`, launchd label `com.aether.daemon` (`internal/syncd/service.go:9,87`) |
| Paths | about 16 | `/etc/aether/server.conf`, `/etc/aether/aether-server.env`, `/var/lib/aether`, `/etc/aether-edge/`, `/var/lib/aether-edge`, the CLI's config directory (`~/.config/aether`, `~/Library/Application Support/aether`, `%AppData%\aether`; `internal/cli/config.go:204-218`), `~/.ssh/aether_config`, `/run/aether`, `/aether-cache` |
| Git names | branch prefix, about 12 ref names, 5 config keys, remote `aether` | `internal/gitengine/slug.go:62`, `internal/gitengine/snapshot_retention.go:16-20`, `internal/gitengine/checkout.go:22-24`, `internal/localops/pull.go:97` |
| Hook files in agent homes | 3 file names | `internal/coordcli/hook_install.go:180-201` |
| Install scripts | 2 | `scripts/install.sh`, `scripts/install.ps1`, fetched from `raw.githubusercontent.com/3xDevOps/Aether/main/scripts/` |

### Resolved by existing installs

About 120 names.

| Group | Count | Defined at |
| --- | --- | --- |
| Release repository | 1 | `internal/selfupdate/selfupdate.go:26` (`Repo`), `scripts/install.sh:29`, `android/.../UpdateTarget.kt:28-29` |
| Release files | 18 | `.github/workflows/release.yml:936-958`; fetched by name at `internal/selfupdate/check.go:70`, `internal/selfupdate/manifest.go:185`, `internal/serverupdate/preflight.go:26-31` |
| Signed manifest | 1 name, 3 image keys, 1 public key | `internal/selfupdate/manifest.go:24`, `:117`, `internal/selfupdate/keys.go:13` |
| Container images | 3 | `ghcr.io/3xdevops/aether-standard` (`internal/server/server.go:59`), `aether-browser` (`internal/server/browser_config.go:5`), `aether-edge` (`internal/selfupdate/manifest.go:114`) |
| Edge update signer | 2 strings | `scripts/edge-update.sh:20-21` |
| Hosted edge addresses | 2 defaults | `internal/edge/client/edgeclient.go:30`, `internal/edge/agent/agent.go:26` |
| Edge cookies | 3 | `internal/edge/service/web.go:22-26` |
| Link schemes | `aether://run`, `join`, `claim` | `desktop/electron-builder.yml:35-38`, `android/app/src/main/AndroidManifest.xml:61-68`, `internal/edge/service/pages.go:221` |
| SSH banner and subsystems | 1 and 10 | `internal/cli/dial.go:23`, `internal/sshd/clientgate.go:20`, `internal/protocol/protocol.go:19` |
| Signing context strings | 7 | `internal/edge/proto/enroll.go:18`, `grant.go:38`, `connector.go:57,75`, `internal/store/device.go:24`, `internal/connector/connector.go:361` |
| HTTP headers | 5, and a sixth since | `internal/edge/proto/api.go:69,75` |
| Edge metric names | `aether_edge_*` | `cmd/aether-edge/metrics.go:204` |
| Connector resource address | 1 per server | each server binds connector tokens to `<its edge address>/mcp` (`internal/connector/connector.go:156-160`, `authorize.go:186`) |
| Server state on disk | about 8 | `/var/lib/aether`, `aether.db` (`internal/disk/disk.go:86`), `aether.db.pre-update` (`internal/serverupdate/trial.go:70`), unit name (`internal/serversetup/unit.go:11`) |
| Docker labels, names, networks | about 14 | `internal/runtime/docker.go:31-44`, `internal/runtime/docker_network.go:16,22`, local image prefix `aether/` (`internal/runtime/docker_image.go:19-24`) |
| VM names | 4, plus the guest API | `internal/runtime/vm/unit.go:28`, `internal/runtime/vm/network.go:34-38`, `internal/guest/guestpb/` |
| App identities | about 10 | Android `io.aether.android` (`android/app/build.gradle.kts:43,50`), desktop `io.aether.desktop` (`desktop/electron-builder.yml:8`) |
| Browser storage | 33 keys and message types | `web/src/lib/api.ts:359` (`aether.token`), `web/public/sw.js:122-125` |

### Source only

| Name | Count | Defined at |
| --- | --- | --- |
| Go module path `github.com/3xDevOps/Aether` | 3,990 import lines, 1,422 files | `go.mod:1`; also `Makefile:8`, `.golangci.yml:23-48`, `.github/workflows/ci-suite.yml:413-422` |
| Version stamp flags with the module path written out | 4 sites | `.github/workflows/windows-install.yml:67,188`, `images/edge/Dockerfile:26`, `scripts/deploy.sh:84` |
| Paths containing the name | 229 | `cmd/aether*`, `android/.../io/aether/android/`, `packaging/systemd/` |
| Go identifiers | about 20 | `AetherRemoteURL` (`internal/localops/push.go:136`) |
| Proto package | 3 files | `internal/guest/guestpb/*.proto` (`aether.guest.v1`) |
| Image paths in workflows | about 45 lines | `.github/workflows/release.yml`, `promote-edge.yml` |
| Dashboard package name | 1 | `web/package.json:2` |

### The domain

`onaether.dev` appears on 100 lines in 29 files: 71 lines in docs, 12 in
product code and packaging, the rest in tests. The hostnames in use:

| Hostname | Serves | Named in |
| --- | --- | --- |
| `onaether.dev` | the website | both connector listings |
| `edge.onaether.dev` | the hosted edge's relay, its connector endpoint `/mcp`, device approval `/device` | the two compiled defaults above, `scripts/install.sh:743`, 2 listings, 57 doc lines |
| `auth.onaether.dev` | the hosted edge's sign-in, Devices, Servers and Account pages | docs only; set on the edge host through `AETHER_EDGE_SIGNIN_ORIGIN` |
| `team@onaether.dev` | contact mail | `docs/privacy.md:15`, `docs/legal/`, both listings |

### The website

The website is built from a separate repository. It carries the name in its
copy, its address in its metadata, the install command, a link to this
repository, the old mark, and a copy of the dashboard that it shows as a
demo. Its inventory and pull requests are kept in that repository.

## What stays `aether`

Each is read by a program that may be older or newer than the one that
wrote it. Operators do meet some of them (`/etc/aether/server.conf`,
`/var/lib/aether`, `docker ps`, the release page); the docs name them as
they are.

- `aether-release.json`, its `.sig`, `checksums.txt` and the 15 binary file
  names of a release.
- The SSH banner `SSH-2.0-Aether`, the 10 subsystem names, the 6 headers and
  the 7 signing context strings.
- `/var/lib/aether`, `aether.db`, `aether.db.pre-update`, `/etc/aether`,
  `aether.slice`.
- Docker labels, container and network names, and the local image prefix
  `aether/`. A changed prefix would make saved environment images look like
  registry images and pull them from Docker Hub.
- `refs/aether/*` and the `aether.*` git config keys in every checkout.
- The client's git remote `aether` (item 24 as built).
- In-container paths `/run/aether`, `/opt/aether`, `/aether-cache`.
- Edge cookies, dashboard storage keys and service-worker message types.
  Renaming them signs everyone out for nothing.
- The edge image name, its binary, container and service name
  `aether-edge`, its paths and metric names `aether_edge_*`, and the
  promotion predicate type
  `https://github.com/3xDevOps/aether/edge-promotion/v1`, which is an
  opaque string and does not follow the repository's name.
- The sync daemon's service names: `aether-daemon` and `com.aether.daemon`.
- The connector address `https://edge.onaether.dev/mcp` in the Claude and
  ChatGPT listings ([The domain move](#the-domain-move)).
- The Android application id and the desktop app id
  ([decision 5](#5-android-application-id)).

## Compatibility of each changed name

"Old install" means any release up to `v0.8.7-beta.14`. Every one of them
finds updates the same way: it follows
`github.com/3xDevOps/Aether/releases/latest` to the newest tag, fetches
`aether-release.json` and its signature for that tag, then downloads files
by their compiled-in names (`internal/selfupdate/manifest.go:216-247`).
There is no stepping: an old install always jumps to the newest release, so
no "bridge release" can exist once a newer one is published. An install
that already has release-channels pull request 2 reads the signed channel
pointers under `releases/download/channels/` instead; those follow the same
redirect after a repository rename.

| Name | How an old install keeps working | How the new name arrives | When the old one goes |
| --- | --- | --- | --- |
| Release repository | GitHub redirects `releases/latest`, release downloads, the API and raw file addresses of a renamed repository. The updater follows redirects (`internal/selfupdate/selfupdate.go:41-53`), as do `curl -L` in `install.sh:340` and `install.ps1` | After the rename, `Repo` and the installer defaults change to the new name | Never: the old repository name must never be reused |
| Release files and manifest | Unchanged names | Not renamed | Never |
| Binaries on a server or client | The updater writes over the files it finds. Every host, old or new, keeps the real files `aether` and `aether-server` | `cloudfield` and `cloudfield-server` are symlinks to them, created when missing by the installer, by the updater, by `aether-server serve` when systemd starts it (which also links the CLI when the real `aether` is beside it), and by `aether update`, which uses the same authorization as for the binary where the directory is not the user's (`internal/macinstall`, `sudo` on Linux) and does so even when already up to date. That covers an install that an older updater brought straight to a release from phase 3 or later. A CLI that finds the link missing and cannot create it prints the exact `ln -s` line once. The server binary never creates links when run as `sudo`, `aether-internal`, `dev-exec`, `acp-keeper`, `guest` or `mcp`, which run inside sessions. On Windows, where a per-user install cannot create symlinks, `cloudfield.cmd` calls `aether.exe`. One layout everywhere means a downgrade to any old release still finds its files | Never |
| Server unit | An old binary that returns after a failed update trial runs `systemctl restart aether-server` (`internal/serverupdate/host.go:68,96`). The unit file stays `aether-server.service` with its text unchanged, on every host | The installer, the updater and `aether-server serve` under systemd, all running as root, link `/etc/systemd/system/cloudfield-server.service` to it. The installer and the updater reload systemd; the server does not, because a reload has host-wide effects (`cmd/aether-server/main.go:537-541`), and instead warns with the exact `sudo systemctl daemon-reload` line when systemd does not resolve the name. Printed and documented `systemctl` lines say `cloudfield-server` only once that is in a stable release | Never |
| `aether-internal` in sessions | The path is fixed when a container is created. Hook and plugin files in member homes call `/usr/local/bin/aether-internal`, and a home is shared with containers made by any server version | New containers also get `/usr/local/bin/cloudfield-internal`. Prompts name it only in a container that has it. Everything written into a home keeps calling the old path | Never |
| `AETHER_*` read by a program | Still read | The reader takes `CLOUDFIELD_X` when set, else `AETHER_X` | Never |
| `AETHER_*` set inside sessions | Still set, under the old name only. Readers accept both, and setting one name keeps an override such as `AETHER_X=… cmd` unambiguous | Docs name the variable `AETHER_RUN_ID` and its siblings as they are | n/a |
| Variables in a host's env files | An edge host or server that rolls back runs a binary that reads `AETHER_*` only | The docs keep `AETHER_*` for `/etc/aether/aether-server.env`, `/etc/aether-edge/aether-edge.env` and the edge updater's file until the release that reads both names is the oldest a rollback can reach | n/a |
| The CLI's config directory | Still read | The CLI uses the `cloudfield` directory when it exists, else the old one. It never moves the directory, because an older CLI on the same machine would lose its links | Never |
| Hosted edge address | The edge keeps serving `edge.onaether.dev` and `auth.onaether.dev`. A server that enrolled there keeps receiving grants issued in that name | [The domain move](#the-domain-move) | Not while the hosted edge exists |
| Connector address | A server accepts connector tokens only for `<its edge address>/mcp` | A server learns every address of its edge and accepts each; the listings keep the old address | [The domain move](#the-domain-move) |
| Edge update signer | An edge host's `edge-update.sh` accepts promotions signed by a workflow in `3xDevOps/aether` only | A script that accepts that identity and the renamed repository's, installed on every edge host before the first promotion after the rename. `scripts/edge-attestation.sh` accepts a build attested under either name, because a promotion must still verify images built before the rename; `promote-edge.yml` verifies its own run under the current name only | When no edge host runs the old script |
| Container images | Each server pulls the image tagged with its own version, already published. Packages belong to the organisation, not the repository | Not renamed | Never deleted |
| Link scheme `aether://` | Old apps handle `aether://` only, so the edge keeps emitting it | The desktop and Android apps also register `cloudfield://` | Never emitted less |
| Branch prefix `aether/run-` | Existing sessions and open pull requests keep their branches | The server accepts both prefixes everywhere; new sessions get `cloudfield/run-` ([decision 8](#8-names-people-see-inside-sessions)) | Never |
| MCP server and tool names | Unchanged | [Decision 8](#8-names-people-see-inside-sessions) | n/a |
| Desktop product name | The app's bundle name and install folder are named after it. Its data directory is named after `productName` in `desktop/package.json`, else `name` there: `aether-desktop` today | The new app uses the old data directory when the new one does not exist. `aether gui` finds, stops and rebuilds the app under either name. On a machine that has the old app it installs the new one beside it and removes the old one after the new one has started once; a Dock or taskbar pin has to be made again | Never |

Two failures to design against, both found in the code:

- **A renamed manifest fails silently.** A missing `aether-release.json`
  reads as "no release", which the updater reports as up to date
  (`internal/selfupdate/manifest.go:44,220-226`).
- **The updater does not resolve companion binaries.** It resolves its own
  path through symlinks but writes its companions at
  `filepath.Join(dir, name)` (`internal/selfupdate/update.go:181-187`,
  `internal/serverupdate/apply.go:218`). On a host where `aether` is a
  symlink to `cloudfield`, an update would replace the symlink with a file
  and leave `cloudfield` stale, and no fix can reach a release already
  published. So the real files keep the old names on every host, and only
  the symlinks carry the new ones. Pull request 2 makes the updater create
  them and never write through one.

## Phases

Each phase ships alone. A phase starts only when the shims it names are in a
published release.

### Phase 0: shims

Nothing a person sees changes. One release carries:

- the updater, the server at start and `aether update` creating the
  new-name symlinks and the unit link;
- the server binary answering to `cloudfield-internal`, and new containers
  getting both in-container paths;
- the variable rule: read the new name, else the old;
- the edge serving several address pairs, and the server's connector
  accepting each ([The domain move](#the-domain-move)), deployed with the
  old pair only;
- `edge-update.sh` and `edge-attestation.sh` accepting both signer
  identities;
- the server and CLI accepting `cloudfield/run-` branches;
- the desktop and Android apps registering `cloudfield://`, the desktop
  data-directory fallback, and `aether gui` removing the old app.

### Phase 1: the name people read

The first Cloudfield release. Dashboard, CLI output, error messages, edge
pages, agent-facing text, docs, README, app names, icons and listings say
Cloudfield. Commands, variables and addresses in that copy are still the old
ones; the docs say so in one line at the top of
[docs/install.md](../install.md).

The sweep is five pull requests by area, each made with a scripted replace
checked against [What stays `aether`](#what-stays-aether). Each conflicts
with every open pull request in its area, so it is rebased and merged
before other work there. A last pull request adds a check to
`scripts/public-audit.sh` that fails on the capitalised old name in copy:
dashboard strings, edge page templates and listings. It widens to docs
after phase 3. The existing pattern at `scripts/public-audit.sh:56` is not
touched.

Needs from phase 0: the desktop data-directory fallback.

### Phase 2: the domain

[The domain move](#the-domain-move). Needs from phase 0: the multi-address
edge and connector. The hosted edge follows the stable channel, so this
phase starts once the release that carries them is stable and promoted to
the edge's `:stable` image.

### Phase 3: the names people type

Docs, help text and agent prompts use `cloudfield`, `cloudfield-server`,
`cloudfield-internal` and, for variables set in a shell, `CLOUDFIELD_*`.
Old names keep working and are no longer documented, except in one table
in [docs/install.md](../install.md) that maps old to new.

Needs from phase 0: the symlinks, the unit link, both in-container paths
and the variable rule, all in a **stable** release. After the first stable
exists, an old install reaches only stable releases through
`releases/latest`, and a documented command must exist on every host that
has updated.

### Phase 4: the repositories

This repository was renamed to `3xDevOps/cloudfield` on 2026-11-16, before
pull request 7 had merged. No promotion had ever run, so no edge host was
refused one. What that leaves: every edge host gets the script from pull
request 7 before the first promotion, because a host with the older script
refuses a promotion signed under the new name and prints `has no valid
promotion attestation`; and `3xDevOps/aether` stays an accepted signer on
hosts until `:stable` carries a release promoted after the rename.

Still owed, as soon as each can land: pull request 22 changes the
repository name wherever it is written, except the promotion predicate
type, and the website's repository link changes with it, because its GitHub
statistics come from a search by repository name and search does not follow
a rename. The Go module path follows in its own pull request
([decision 1](#1-how-deep-the-rename-goes)). The website repository's own
rename belongs to the website's plan.

Conditions that hold from now on: the old repository names are never
created again, the organisation is not renamed, old releases and tags are
not deleted, and the release signing key and the `channels` release stay in
the renamed repository.

### Phase 5: names inside sessions

New sessions get `cloudfield/run-` branches. The MCP server and tool names
stay ([decision 8](#8-names-people-see-inside-sessions)); the connector's
title changed in phase 1.

## The domain move

### What must exist on cloudfield.ai first

| Item | Why |
| --- | --- |
| `cloudfield.ai` serving the website | the site's canonical address |
| `edge.cloudfield.ai` and `auth.cloudfield.ai` resolving to the hosted edge | the edge obtains its own certificates, which needs the names to resolve to it first |
| A second GitHub OAuth app with the callback `https://auth.cloudfield.ai/signin/github/callback`, and its secret on the edge host | an OAuth app has one callback address (`internal/edge/service/oauth.go:34,53`). Edge accounts are keyed by GitHub user id (`:228-229`), so both apps sign the same person into the same account |
| `team@cloudfield.ai` receiving mail, with SPF, DKIM and DMARC | the contact address in the privacy pages and listings |
| The OpenAI domain challenge for the new relay host | [docs/chatgpt-plugin.md](../chatgpt-plugin.md) |

### Edge code

Today the edge serves exactly one sign-in address and one relay address and
answers 421 to any other host
(`internal/edge/service/service.go:150-171,313-332`). The edge client
follows no redirects (`internal/edge/client/edgeclient.go:66`). A server
signs its enrollment over the edge's address (`internal/edge/proto/enroll.go:76-80`)
and checks that every grant names that address as issuer
(`internal/edge/agent/relay.go:35-36`). So the address cannot simply move,
and a redirect cannot help.

The edge change, one pull request:

- The edge takes a list of address pairs in a new setting. The two
  existing settings keep naming the old pair, so an edge host that rolls
  back to an older image still serves it.
- A request is served by the pair its host belongs to: sign-in pages,
  connector metadata, cookies, join links, certificates
  (`cmd/aether-edge/main.go:227`) and the 421 text. No pair is preferred.
- A server's registration records the address it enrolled at. Every grant
  for that server names that address as issuer, whichever address the
  client came in on.
- One OAuth app, with its own secret file, per sign-in address.
- The edge tells each server its full list of relay addresses, and counts
  enrolled servers, device sign-ins and connector calls per address.

That is not enough for connectors. A **server** binds every connector
token to `<its edge address>/mcp`: it refuses any other resource
(`internal/connector/authorize.go:186`), stamps that address as issuer
(`:499`) and refuses a token stored for another
(`internal/connector/token.go:77,132`). So a connector that Claude or
ChatGPT registered at the old address fails against a server that moved,
and a connector at the new address fails against a server that has not.
The server change, one pull request:

- The connector accepts the resource address of every relay address its
  edge announced, keeps each token bound to the address it was issued
  for, and answers metadata in the address the client used.
- A server that moves keeps accepting the address it moved from.

The edge host's updater, in pull request 7: `edge-update.sh --rollback`
checks only database compatibility today (`scripts/edge-update.sh:582-601`).
With the address list set on the host, it refuses to go below the
multi-address release unless a new flag forces it. The automatic rollback
after a failed update is exempt: it only returns to the release that ran
just before, and refusing it would leave a failed edge running.

The edge's key does not change, and a server pins that key per edge, not per
address (`internal/edge/agent/state.go:127`), so re-enrolling at the new
address keeps the pin.

### Order of the switch

Each step can be undone.

1. Create the DNS names, the OAuth app, the mailbox and its mail records.
2. Website: publish the rebranded site with its address set to
   `https://cloudfield.ai`, attach `cloudfield.ai` to it, and check it.
   Then turn `onaether.dev` into a redirect to the same path on
   `cloudfield.ai`.
3. Edge: deploy a release that has pull requests 5 and 6 (edge protocol
   6) with only the old pair. Nothing changes.
4. With pull request 7's `edge-update.sh` on the host and the
   multi-address release past its update, add the new pair. Test both
   directions: a server enrolled at the old
   address reached from a CLI signed in at the new one, and the reverse;
   a connector at each address against a server enrolled at the other; a
   join link and a claim code from each.
5. Release with `https://edge.cloudfield.ai` as the default for **new**
   setups, `cloudfield-server edge move <address>` for existing servers,
   and the new hostnames in the docs. `aether-server setup` writes the
   address into `/etc/aether/server.conf`
   (`cmd/aether-server/setup.go:363-368`), so an existing server keeps the
   old one until an admin moves it. Code that treats the default address
   specially (`cmd/aether-server/install.go:364`,
   `internal/sshd/server.go:398`, `cmd/aether-server/setup.go:369`) treats
   both hosted addresses alike.
6. The Claude and ChatGPT listings keep `https://edge.onaether.dev/mcp`.
   A server older than the connector change accepts no other address, and
   the directory sends every user to one address. The listings move to the
   new address when the per-address counters show no connector calls
   reaching servers below that release. Their name and description change
   in phase 1, which needs a directory re-review
   ([decision 11](#11-first-release-and-directory-listings)).

### How the old hostnames keep working

- `onaether.dev` stays registered and renewing with no end date. Whoever
  held it later could not forge grants, because servers pin the edge's key,
  but would receive device tokens and connector sign-ins from every install
  that still dials it.
- `edge.onaether.dev` and `auth.onaether.dev` stay served by the hosted
  edge. Retiring them would cut off every server that has not moved, every
  CLI link made there, every connector registered there and every printed
  join link, so they are not retired while the hosted edge exists
  ([decision 7](#7-repository-names-and-the-old-domain)).
- `team@onaether.dev` keeps receiving.
- A self-hosted edge uses its operator's own addresses and is not affected.

## The website and the logo

The website's rebrand is planned and built in its own repository. What
this plan needs from it:

- the site's address, contact address and crawler files (`llms.txt`,
  `agents.txt`) name Cloudfield and `cloudfield.ai` before `onaether.dev`
  becomes a redirect;
- the install command and the repository link change in the hour of the
  repository rename ([Phase 4](#phase-4-the-repositories));
- its copy of the dashboard is refreshed, and its screenshots re-shot,
  after phase 1 lands here.

The logo's author delivers the mark and three logo variants as SVG. Every
icon this repository ships is generated from `web/public/aether-mark.png`
by `scripts/make-icons.py` ([CONTRIBUTING.md](../../CONTRIBUTING.md)): the
dashboard's `web/public/icons/`, the desktop's `desktop/build/icon.ico` and
`desktop/build/icons/`, the Android launcher `mipmap-*` and the Play
feature graphic.

1. Render the mark's SVG to a transparent PNG, commit it in `web/public/`
   and point `scripts/make-icons.py` at it. The script opens its source
   with Pillow (`load_mark`, `:120`), which does not read SVG. The script
   also writes the wordmark (`FEATURE_WORDMARK`, `:100`) and the tagline
   into the Play graphic.
2. Run it and commit what it writes.
3. Redraw by hand what the script does not make: the Android notification
   icon `ic_stat_aether.xml` (traced from the mark),
   `docs/assets/aether-banner.svg` and `docs/media/aether-mark.png`.

The mark has to work in three places the old one was drawn for: as a
single-colour mask (the dashboard top bar and the Android notification
icon), inside a maskable icon's safe zone, and at 16 pixels. If it does
not, the fix goes back to the logo's author, not into the script. The
dashboard, desktop and Android changes each get the design review loop.

## Sequencing with work in flight

### Release channels

[2026-11-15-release-channels.md](2026-11-15-release-channels.md) is pull
request 943 and its eleven implementation pull requests.

- Its new runtime names are neutral and stay: the `channels` release,
  `beta.json`, `stable.json`, `nightly.json`, `release_channel`. Their
  address contains the repository name and follows GitHub's redirect.
- It names one new secret, `AETHER_NIGHTLY_SIGNING_KEY`, which does not
  exist yet. That plan should name it `NIGHTLY_SIGNING_KEY` before its
  pull request 9 creates it.
- Its pull requests 1, 2 and 6 rework `internal/selfupdate` and
  `release.yml`. The updater change (pull request 2 here) lands after its 1,
  2 and 3. Nothing here touches `release.yml`, because no release file is
  renamed.
- Its pull requests 7 and 8 edit `promote-edge.yml` and
  `scripts/edge-update.sh`. The two-identity change goes directly after
  them, so operators copy the script once.
- As released: `v0.8.7`, the first stable, carries phase 0 and phase 1
  except item 13. Its dashboard, CLI, docs and Android app say Cloudfield;
  its desktop app is still named Aether (#1010 merged after the tag).
  Because phase 0 is in that stable release, phase 3 lands on `main` in the
  `v0.9.0` line.
- Its pull requests 4 and 11 add dashboard copy and docs that say Aether.
  The phase 1 sweep runs after them, and the name check keeps later ones
  clean.
- The repository was renamed on 2026-11-16, after its pull requests 6 and
  7 had merged ([Phase 4](#phase-4-the-repositories)).

### Micro VM sessions

[2026-11-13-microvm-sessions.md](2026-11-13-microvm-sessions.md) introduces
`aether-internal guest`, the guest API package `aether.guest.v1`, kernel
command-line keys `aether.*`, `aether-vm-<key>.service`, the tap and
firewall table names, and the release file `aether-vmlinux-x86_64`. None is
in a published release yet. All are read only by programs, so by
[the rule](#the-rule) they stay ([decision 9](#9-micro-vm-names)).

- The phase 1 Go sweep does not touch `internal/runtime/vm`,
  `internal/guest` or `internal/vmimage` while that stack is open. Its
  work item 12 (wording) uses the new name.
- Its helper is the server binary run as `aether-internal`, so the
  `cloudfield-internal` name is answered with no change there, and a VM
  run gets both in-VM paths the way it gets the old one
  ([found while building](#found-while-building-phase-0)).
- Its work item 10 owns `cmd/aether-server` and `internal/serversetup`, and
  item 7 owns mounts. Pull requests 2 and 3 here touch the same files and
  are agreed with that stack's lead before they open.

## Decisions (2026-11-16)

The owner chose the recommended option of each. The options not taken are
listed with what they would have cost.

### 1. How deep the rename goes

Brand, commands and variables, with the Go module path as the last step.
The module path is source only and mechanical, but it touches 1,422 files,
so it needs a window with the merge queue empty.

| Not taken | Cost |
| --- | --- |
| Brand only: copy, docs, logos, app names, website, domain | People install Cloudfield and type `aether` |
| Everything: also release files, images, data paths, app ids, wire names | Doubles every release, adds migrations of `/var/lib/aether` and the database with rollback risk, and makes a second Android and desktop app. None of it is visible to a person |

### 2. Command names

`cloudfield`, `cloudfield-server` and `cloudfield-internal`. No short alias
ships: `cf` is already the Cloud Foundry CLI and Cloudflare's CLI. The edge
command stays `aether-edge`: it runs inside the edge's container, and the
updater script on every edge host calls it by that name.

### 3. Old command names

The old names stay the real files and the new names are links, with no end
date. The same holds for in-container paths and `AETHER_*` variables.
People see `aether-server` in `ps` and in `/usr/local/bin`.

| Not taken | Cost |
| --- | --- |
| New installs get real files under the new names | A downgrade to a release from before the rename writes through the links and corrupts the layout. Needs a floor on downgrades in both updaters and the install script |
| Remove old names at 1.0 | Breaks scripts, hook files in member homes and every host installed before the rename. Needs a migration command |

### 4. Spelling

- "Cloudfield": one word, capital C only. Never "CloudField" or "Cloud
  Field". Lowercase `cloudfield` in commands, paths and addresses.
- "Cloudfield Edge" for the relay as a product and for the hosted service;
  "an edge" for one running relay, as the docs do today. "Edge" alone as a
  proper name reads as Microsoft Edge in a sentence about browsers and
  sign-in.
- "Enhanced", "Standard" and "Assistant" stay. "Ask Aether" becomes "Ask
  Cloudfield".
- Other parts are plain nouns after the name: Cloudfield server, Cloudfield
  dashboard, Cloudfield for Android, Cloudfield desktop app.
- Hostnames: `cloudfield.ai`, `edge.cloudfield.ai`, `auth.cloudfield.ai`.
  Contact: `team@cloudfield.ai`.

### 5. Android application id

`io.aether.android` (`android/app/build.gradle.kts:50`) and the desktop app
id `io.aether.desktop` both stay. The Android id appears in the Play Store
address and in Android's app details for good.

Not taken: `ai.cloudfield.android` before the first Play publication. A
changed id is a different app: every current install would keep the old
app, which never updates again, and each person would install the new one
and link it again. A changed desktop id makes a second app and drops
taskbar pins and notification permission.

### 6. Release files and images

The `aether-*` release files and image names stay. The release page and
`docker ps` show the old name.

| Not taken | Cost |
| --- | --- |
| Publish release files under both names | 15 more files per release, with their verification time, with no end date: an old updater cannot learn the new names |
| Rename | Strands every old install; a renamed manifest does so silently |

### 7. Repository names and the old domain

This repository becomes `cloudfield`, in the same organisation. No
repository is created at an old name again. `onaether.dev` stays registered
with no end date, and the old edge addresses stay served while the hosted
edge exists.

### 8. Names people see inside sessions

- **Branch prefix.** New sessions get `cloudfield/run-…`; both prefixes are
  accepted. Existing sessions keep `aether/run-…`.
- **MCP names.** The connector's display title becomes Cloudfield. The
  server name `aether` and the 55 `aether_*` tool names stay. Renaming them
  would cost every member's saved approvals for those tools, and four more
  characters on each of 55 names against a tool-description budget that is
  nearly full.

### 9. Micro VM names

The `aether` names that stack introduces stay. An operator sees only
`aether-vm-<key>.service`, next to an `aether.slice` that existing hosts
keep anyway.

### 10. Moving existing servers to the new edge address

An admin runs `cloudfield-server edge move`. A server that moves by itself
when the edge announces a preferred address can follow in a later release,
if the per-address counters show servers staying behind.

### 11. First release and directory listings

- Decided: `v0.9.0` is the first release that says Cloudfield, and
  `v0.8.7`, the first stable, ships as Aether. As released, most of phase 1
  merged before `v0.8.7` was cut, so it says Cloudfield everywhere but the
  desktop app's name (item 13, #1010, came after).
- The Claude and ChatGPT listings are resubmitted with the new name, title
  and description at phase 1, keeping the old connector address. Each
  directory re-reviews on a lead time the project does not control.

## To verify before building

Stated by the design and not yet tested:

- systemd accepts `show` and `restart` on a unit name that is a link made
  by hand rather than by `systemctl enable`, and whether it resolves a new
  link before a `daemon-reload`.
- GitHub requires a second OAuth app for a second callback host.
- A repository rename leaves the organisation's container packages and
  their Actions access unchanged.
- With release-channels pull request 7, a promotion called through
  `workflow_call` still carries `promote-edge.yml@refs/heads/main` as its
  identity.

### Found while building phase 0

- Electron does not name its data directory after electron-builder's
  `productName`. It uses `productName` in `desktop/package.json`, else
  `name`, and electron-builder copies only `extraMetadata` into the
  packaged file (read in the source of Electron 33 and electron-builder
  26; not checked with a packaged build). The directory is
  `aether-desktop` today, and pull request 9 keeps an existing
  `aether-desktop` directory in use whatever the app is named.
- A server's bare repositories get the two-prefix hook when the server
  starts: it rewrites `hooks/update` in every workspace's repository at
  start and on first use. Every release does this with its own hook text,
  so a server put back to a release below pull request 8 stops refusing a
  force-push to `cloudfield/run-` branches.
- By reading, not by a run: a micro VM run gets both in-VM paths from
  pull request 3 with no change in that stack, because the mount is of the
  server-binary kind, which the guest helper binds itself.
- The updater never fails an update over a link it cannot create. An
  update that runs without administrator rights is refused before it
  downloads when `aether` is itself a link into a directory it cannot
  write. `aether-server update` and the installer reload systemd; the
  server's own updater creates the unit link and does not, so after an
  update from the dashboard the new unit name works only if systemd
  resolves a hand-made link without a reload. That is still untested
  ([To verify before building](#to-verify-before-building)).
- The CLI's `cloudfield` config directory is matched by exact name. The
  desktop app's data directory sits beside it, and macOS and Windows
  ignore case there, so that directory can never be named `Cloudfield`.
  Open for the owner: it stays `aether-desktop` for every install, which
  needs no code and is what pull request 13 does unless told otherwise, or
  new installs get `cloudfield-desktop`, which means a new `name` in
  `desktop/package.json` and no `productName` there.
- A doc line that quotes what the product shows changes in the pull request
  that changes that string. Pull request 15 lists those lines for 11, 12,
  13 and 14, whose file lists gain them.
- Not checked on real machines, owed before pull request 13: macOS 13 and
  later may refuse a renamed app moving `/Applications/Aether.app`; how a
  packaged build's `Info.plist` reads; Windows refusing to rename a running
  app's folder; a real relaunch after an update on Linux and macOS.

## Implementation pull requests

Each is one session. Phase and dependencies are in brackets.

1. `docs(plans)`: this plan.
2. `feat(selfupdate)`: the updater, `aether-server serve` under systemd and
   `aether update` create the `cloudfield` and `cloudfield-server` symlinks
   beside the real files (`cloudfield.cmd` on Windows) and the
   `cloudfield-server.service` link when they are missing, and never write
   through a symlink. Tested with a direct update from a release older
   than this one. `internal/selfupdate/update.go`,
   `internal/serverupdate/apply.go`, `serverupdate.go`, `host.go`,
   `internal/macinstall`, `cmd/aether/update.go`,
   `cmd/aether-server/update.go`, `main.go`. [0; after release-channels 1, 2 and 3;
   agreed with micro VM item 10]
3. `feat(server)`: answer to `cloudfield-internal`; mount and reserve both
   in-container paths. `cmd/aether-server/main.go`,
   `internal/coordtransport/paths.go`, `internal/runtime/mounts.go`,
   `internal/scheduler/coordination.go`, `assistant_container.go`,
   `docs/coordination.md`, `docs/mcp-bridge.md`.
   [0; agreed with micro VM items 3 and 7]
4. `feat(envname)`: read `CLOUDFIELD_*`, else `AETHER_*`. New
   `internal/envname`; `internal/cli/config.go`, `cmd/aether-server/main.go`,
   `cmd/aether-edge/options.go`, `main.go`, `metrics.go`, `data.go`,
   `internal/selfupdate/selfupdate.go`, `scripts/install.sh`,
   `install.ps1`, `install-sysbox.sh`, `scripts/edge-update.sh`,
   `desktop/main.js`, `android/.../Gateway.kt`, `UpdateCheck.kt`.
   [0; after release-channels 1, 2 and 8]
5. `feat(edge)`: serve several address pairs from a new setting; a grant's
   issuer is the address its server enrolled at; one OAuth app per sign-in
   address; announce the relay addresses to servers; per-address counters.
   `cmd/aether-edge/options.go`, `main.go`, `metrics.go`,
   `internal/edge/service`, `internal/edge/relay`, `internal/edge/proto`,
   `packaging/edge/`, `docs/edge.md`. A registration keeps its address in
   memory, so the edge's database and unit file do not change. [0]
6. `feat(connector)`: the server's connector accepts every relay address
   its edge announced and keeps each token bound to its own.
   `internal/connector/connector.go`, `authorize.go`, `token.go`,
   `internal/sshd/connector.go`, `internal/edge/agent`. It raises the edge
   protocol version, because pull request 5 counts connector calls per
   address and per negotiated protocol version, and step 6 of the switch
   reads that count. The count tells old servers from new only while the
   edge runs a release with this pull request. `internal/edge/proto`.
   [0; after 5]
7. `feat(edge)`: `edge-update.sh` accepts a promotion signed under either
   repository name, and `edge-attestation.sh` a build attested under
   either; `promote-edge.yml` keeps verifying its own run under the current
   name only. `--rollback` refuses to go below the multi-address release
   (edge protocol 6) while the address list is set, unless
   `--drop-origin-pairs` forces it. The script reads `CLOUDFIELD_*` before
   `AETHER_*`. No manual step in `packaging/edge/upgrade.json`: a host with
   the older script fails at the signature, before it reads that file.
   `scripts/edge-update.sh`, `edge-update-test.sh`, `edge-attestation.sh`,
   `edge-attestation-test.sh`, `internal/edge/edgetest/testdata/`,
   `docs/edge.md`. [0; after release-channels 6, 7 and 8]
8. `feat(gitengine)`: accept `cloudfield/run-` branches beside
   `aether/run-`. `internal/gitengine/engine.go`, `transport.go`,
   `internal/syncd/git.go`. [0]
9. `feat(desktop)`: register `cloudfield://`, fall back to the old data
   directory, find, stop and rebuild the app under either name.
   `desktop/electron-builder.yml`, `desktop/main.js`,
   `internal/localops/desktop.go`, `rebuild.go`,
   `internal/localgw/rebuild.go`, `scripts/install.ps1`,
   `desktop/deep-link.js`, `user-data.js`, `sidecar-exit.js`, `embed.go`,
   `internal/localops/desktop_handoff.go`, `cmd/aether/update_app.go`. [0]

   9b. `feat(desktop)`: remove the old app after the new one has started
   once. `cmd/aether/gui.go`, `internal/localops`.
   [0; after 9 and release-channels 4]
10. `feat(android)`: register `cloudfield://`.
    `android/app/src/main/AndroidManifest.xml`, `DeepLink.kt`. [0]
11. `feat(dashboard)`: Cloudfield name, mark and icons. `web/src/` copy,
    `web/src/app/layout.tsx`, `manifest.ts`, `web/public/sw.js`,
    `web/public/` mark and `icons/`, `scripts/make-icons.py`, unit and
    end-to-end tests. Design review. [1; after release-channels 4]
12. `feat(cli)`: Cloudfield in CLI, server and edge messages, edge pages,
    assistant instructions, skill text, connector title. Strings in
    `cmd/`, `internal/cli`, `scheduler`, `assistant`,
    `edge/service/templates`, `coordhooks`, `coordcli`, `ops`, `connector`,
    `mcpbridge`, and their tests. Not `internal/runtime/vm`,
    `internal/guest`, `internal/vmimage`. [1; after release-channels 3 and 4]
13. `feat(desktop)`: product name, dialogs and icons; Windows resources.
    `productName` in `desktop/electron-builder.yml`, `main.js`,
    `desktop/build/`, `packaging/windows/versioninfo.json`,
    `scripts/update-smoke.ps1`, and the app paths in `docs/install.md`.
    `desktop/package.json` is unchanged, so the data directory stays
    `aether-desktop`. Design review. [1; after 9 and 9b are released]
    As built (#1010): install locations follow the product name
    (`~/.local/share/cloudfield/desktop`, `Cloudfield.app`,
    `%LOCALAPPDATA%\Programs\Cloudfield Desktop`); the data directory
    stays `aether-desktop` for every install.
14. `feat(android)`: app name, strings, launcher and notification icons,
    Play listing. `android/app/src/main/res/`, `android/listing/`. Design
    review. [1]
15. `docs`: Cloudfield in `README.md`, `CONTRIBUTING.md`, `AGENTS.md`,
    `CLAUDE.md`, `AI_POLICY.md`, `docs/*.md`, `docs/legal/`,
    `docs/assets/`, `docs/media/`, and the name and description in
    `packaging/claude-connector/` and `packaging/chatgpt-plugin/`. Not
    `docs/plans/`. [1; after release-channels 11]
16. `build`: fail `make public-audit` on the capitalised old name in
    dashboard strings, edge templates and listings.
    `scripts/public-audit.sh`. [1; last]
17. `feat(edge)`: `edge.cloudfield.ai` as the default for new setups,
    `edge move`, both hosted addresses treated alike, new hostnames in
    docs. `internal/edge/agent/agent.go`,
    `internal/edge/client/edgeclient.go`, `cmd/aether-server/edge.go`,
    `setup.go`, `install.go`, `internal/sshd/server.go`,
    `scripts/install.sh`, `install-test.sh`, `docs/*.md`, `docs/legal/`.
    `edge move` refuses or warns unless the edge negotiates protocol 6 or
    later and announces both addresses: otherwise the moved server accepts
    connector tokens only for the address it is enrolled at.
    As built (#1003): `edge move` refuses, with no override, when the
    target presents a different edge key than the pin, when no key is
    pinned, below protocol 6, or when the edge does not announce both
    addresses. It does not prove the target holds the edge's private key:
    the handshake carries no edge signature, and the target's TLS
    certificate authenticates it as at first enrolment. A proof needs an
    edge protocol change and is the owner's decision.
    [2; after steps 1 to 4 of the switch]
18. `feat(cli)`: installers create the new command names and the unit
    link; printed `systemctl` lines use the new name. `scripts/install.sh`,
    `install.ps1`, `internal/macinstall`, `cmd/aether-server/install.go`,
    `internal/localgw/update.go`.
    As built (#1016): systemd refuses `systemctl enable` on an alias name,
    so every `enable` line keeps `aether-server`, and `journalctl -u` and
    printed `status` lines keep it too. Only the restart lines that `aether
    update` and the dashboard print use `cloudfield-server`, and only where
    systemd resolves the link. The unit's names live in
    `internal/serverunit`, so the CLI does not build in the server's setup
    code. [3; after 2, 3 and 4 are in a stable
    release, and after release-channels 1]
19. `feat(server)`: prompts and role cards name `cloudfield-internal` in
    containers that have it. Hook and plugin files written into homes keep
    the old path. `internal/coordhooks/guide.go`, `internal/coordcli/`,
    `internal/harness/`, `internal/scheduler/acp_driver.go`. [3]
20. `docs`: new commands and `CLOUDFIELD_*` in docs, help and usage text,
    and the dashboard's copyable prompt; the old-to-new table; widen the
    name check to docs. `docs/*.md`, usage strings in `cmd/`,
    `web/src/lib/config-prompt.ts`, `scripts/public-audit.sh`. [3]
21. Owner: rename the repositories. This repository: done 2026-11-16. [4]
22. `fix`: the repository name where it is written, except the
    promotion predicate type, the two-name signer checks and the systemd
    unit text, which the server compares byte for byte.
    `internal/selfupdate/selfupdate.go`, `scripts/install.sh`,
    `install.ps1`, `android/.../UpdateTarget.kt`,
    `internal/push/webpush.go`, the links the CLI, the dashboard and the
    edge pages print, docs links, `README.md` badges. [4; after 21]
23. `refactor`: Go module path, with `go.mod`, `Makefile`, `.golangci.yml`,
    `.github/workflows/ci-suite.yml`, `windows-install.yml`,
    `images/edge/Dockerfile`, `scripts/deploy.sh`, and regenerated
    `internal/guest/guestpb`. One scripted change, in a window the integration
    manager names: the merge queue empty and every open pull request
    merged or told to rebase. [4]
24. `feat(gitengine)`: new sessions get `cloudfield/run-` branches and the
    `cloudfield` remote. `internal/gitengine/slug.go`,
    `internal/localops/pull.go`, `push.go`, `compare.go`,
    `cmd/aether/daemon.go`, the dashboard's copyable git commands, which
    name the remote `aether` (`web/src/routes/diff/review-commands.tsx`),
    and the branch rule in `docs/teams.md`. Two limits remain after it: a
    CLI older than 8 does not fetch the new branches until it updates,
    though `aether pull` still does, and a server put back to a release
    below 8 stops refusing a force-push to them.
    As built (#1024): the branch prefix only. The remote stays `aether`,
    with the daemon's `--remote` default, `internal/localops` and the
    dashboard's commands unchanged: a second remote name would split every
    linked clone for no gain a reader sees. A workspace whose repository has
    a branch named `cloudfield` keeps `aether/run-` for new sessions, since
    git cannot create `cloudfield/run-*` beside it.
    [5; after 8 is in a stable release]

The website's pull requests are listed in its own repository.
