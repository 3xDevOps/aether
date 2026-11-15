# Rename to Cloudfield and Move to cloudfield.ai

**Goal:** the product is named Cloudfield everywhere a person reads or types
its name, and lives at `cloudfield.ai`. An install that has not updated yet
keeps running, keeps reaching its edge, and still finds its update.

**Status:** draft for the owner's review, 2026-11-15. The rename and the new
domain are the owner's decisions of 2026-11-15. Everything under
[Decisions for the owner](#decisions-for-the-owner) is open; the rest of the
plan assumes the recommended option of each. No code has changed.

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
| Paths | about 12 | `/etc/aether/server.conf`, `/var/lib/aether`, `~/.config/aether/config.json`, `~/.ssh/aether_config`, `/run/aether`, `/aether-cache` |
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
| HTTP headers | 5 | `internal/edge/proto/api.go:69,75` |
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
| `onaether.dev` | the website | aether-web `src/lib/seo.ts:13-15`, both connector listings |
| `edge.onaether.dev` | the hosted edge's relay, its connector endpoint `/mcp`, device approval `/device` | the two compiled defaults above, `scripts/install.sh:743`, 2 listings, 57 doc lines |
| `auth.onaether.dev` | the hosted edge's sign-in, Devices, Servers and Account pages | docs only; set on the edge host through `AETHER_EDGE_SIGNIN_ORIGIN` |
| `team@onaether.dev` | contact mail | `docs/privacy.md:15`, `docs/legal/`, both listings |
| `aethercloud.dev` | a redirect to `onaether.dev` | nowhere in either repository |

### aether-web

On its `main`: 585 of 804 files contain the name. 498 of them are a copy of
the dashboard under `src/vendor/aether`, refreshed by
`scripts/sync-aether.ts`. The site's own 87 files hold the name in copy,
`src/lib/seo.ts:13-17` (site address, host, contact), `src/lib/theme.ts:101`
(repository link, also the source of the release lookup),
`public/aether-mark-*.png`, `wrangler.jsonc` and `package.json`
(`aether-web`). The new logo is on `main` as four SVG files under
`public/cloudfield/`. Draft pull request 48 there already carries a
Cloudfield rebrand of the site.

## What stays `aether`

None of these is shown to a person in normal use. All are read by a program
that may be older or newer than the one that wrote them.

- `aether-release.json`, its `.sig`, `checksums.txt` and the 15 binary file
  names of a release.
- The SSH banner `SSH-2.0-Aether`, the 10 subsystem names, the 5 headers and
  the 7 signing context strings.
- `/var/lib/aether`, `aether.db`, `aether.db.pre-update`, `/etc/aether`,
  `aether.slice`.
- Docker labels, container and network names, and the local image prefix
  `aether/`. A changed prefix would make saved environment images look like
  registry images and pull them from Docker Hub.
- `refs/aether/*` and the `aether.*` git config keys in every checkout.
- In-container paths `/run/aether`, `/opt/aether`, `/aether-cache`.
- Edge cookies, dashboard storage keys and service-worker message types.
  Renaming them signs everyone out for nothing.
- The edge image name, its binary and service name `aether-edge`, and the
  promotion predicate type.
- The Android application id and the desktop app id
  ([decision 5](#5-android-application-id)).

## Compatibility of each changed name

"Old install" means any release up to `v0.8.7-beta.14`. Every one of them
finds updates the same way: it follows
`github.com/3xDevOps/Aether/releases/latest` to the newest tag, fetches
`aether-release.json` and its signature for that tag, then downloads files
by their compiled-in names (`internal/selfupdate/manifest.go:216-247`).
There is no stepping: an old install always jumps to the newest release, so
no "bridge release" can exist once a newer one is published.

| Name | How an old install keeps working | How the new name arrives | When the old one goes |
| --- | --- | --- | --- |
| Release repository | GitHub redirects `releases/latest`, release downloads, the API and raw file addresses of a renamed repository. The updater follows redirects (`internal/selfupdate/selfupdate.go:41-53`), as do `curl -L` in `install.sh:340` and `install.ps1` | After the rename, `Repo` and the installer defaults change to the new name | Never: the old repository name must never be reused |
| Release files and manifest | Unchanged names | Not renamed | Never |
| Binaries on a server | The updater writes over the files it finds, so an existing host keeps `/usr/local/bin/aether` and `aether-server` | New binaries also answer to `cloudfield`, `cloudfield-server`, `cloudfield-edge` and `cloudfield-internal`. On an existing host the new names are symlinks to the old files. A new install gets real files under the new names and symlinks under the old ones | Never on a host that has them |
| Server unit | An old binary that returns after a failed update trial runs `systemctl restart aether-server` (`internal/serverupdate/host.go:68,96`), so that unit name must keep resolving | The unit gains `Alias=cloudfield-server.service`. A new install gets `cloudfield-server.service` with `Alias=aether-server.service`. New code finds its own unit from its cgroup instead of a fixed name | Never on a host that has it |
| `aether-internal` in sessions | The mount target is fixed when a container is created. Hook files in member homes call `/usr/local/bin/aether-internal` | New containers get both paths. Prompts name `cloudfield-internal` only in a container that has it | Never |
| `AETHER_*` read by a program | Still read | The reader takes `CLOUDFIELD_X`, then `AETHER_X`. Both set to different values is an error naming both | At 1.0, with a startup error that names the new variable |
| `AETHER_*` set inside sessions | Still set | The server sets both names to the same value | Never |
| `~/.config/aether` | Still read | The CLI uses `~/.config/cloudfield` when it exists, else the old directory. It never moves the directory, because an older CLI on the same machine would lose its links | Never |
| Hosted edge address | The edge keeps serving `edge.onaether.dev` and `auth.onaether.dev`. A server that enrolled there keeps receiving grants issued in that name | [The domain move](#the-domain-move) | Not while the hosted edge exists |
| Edge update signer | An edge host's `edge-update.sh` accepts promotions signed by a workflow in `3xDevOps/aether` only | A script that accepts that identity and the renamed repository's, published and installed on every edge host **before** the repository is renamed | When no edge host runs the old script |
| Container images | Each server pulls the image tagged with its own version, already published. Packages belong to the organisation, not the repository | Not renamed | Never deleted |
| Link scheme `aether://` | Old apps handle `aether://` only, so the edge keeps emitting it | The desktop and Android apps also register `cloudfield://` | Never emitted less |
| Branch prefix `aether/run-` | Existing sessions and open pull requests keep their branches | The server accepts both prefixes everywhere; new sessions get `cloudfield/run-` ([decision 8](#8-names-people-see-inside-sessions)) | Never |
| MCP server and tool names | Unchanged | [Decision 8](#8-names-people-see-inside-sessions) | n/a |
| Desktop product name | The app's data directory is named after it | The new app uses the old data directory when the new one does not exist. The CLI looks for the app under both names | Never |

Two failures to design against, both found in the code:

- **A renamed manifest fails silently.** A missing `aether-release.json`
  reads as "no release", which the updater reports as up to date
  (`internal/selfupdate/manifest.go:44,220-226`).
- **The updater does not resolve companion binaries.** It resolves its own
  path through symlinks but writes its companions at
  `filepath.Join(dir, name)` (`internal/selfupdate/update.go:181-187`,
  `internal/serverupdate/apply.go:218`). On a host where `aether` is a
  symlink to `cloudfield`, an update would replace the symlink with a file
  and leave `cloudfield` stale. This is fixed before any install lays out
  new names (pull request 2).

## Phases

Each phase ships alone. A phase starts only when the shims it names are in a
published release.

### Phase 0: shims

Nothing a person sees changes. One release carries:

- the updater fix above;
- the server binary answering to `cloudfield-internal`, new containers
  getting both in-container paths, the unit alias and own-unit lookup;
- the variable rule: read new then old, set both inside sessions;
- the edge serving several address pairs
  ([The domain move](#the-domain-move)), deployed with the old pair only;
- `edge-update.sh` accepting both signer identities;
- the server and CLI accepting `cloudfield/run-` branches;
- the desktop and Android apps registering `cloudfield://`, and the
  desktop data-directory fallback.

### Phase 1: the name people read

The first Cloudfield release. Dashboard, CLI output, error messages, edge
pages, agent-facing text, docs, README, app names, icons and listings say
Cloudfield. Commands, variables and addresses in that copy are still the old
ones; the docs say so in one line at the top of
[docs/install.md](../install.md).

The sweep is four pull requests by area, each made with a scripted replace
checked against [What stays `aether`](#what-stays-aether), each merged the
day it opens because it conflicts with every open pull request in its
area. A last pull request adds a check to `scripts/public-audit.sh` that
fails on the old name outside an allowlist of kept identifiers, so later
work cannot bring it back.

Needs from phase 0: the desktop data-directory fallback.

### Phase 2: the domain

[The domain move](#the-domain-move). Needs from phase 0: the multi-address
edge, deployed.

### Phase 3: the names people type

Installers and the CLI create the new command names. Docs, help text and
agent prompts use `cloudfield`, `cloudfield-server`, `cloudfield-internal`
and `CLOUDFIELD_*`. Old names keep working and are no longer documented,
except in one table in [docs/install.md](../install.md) that maps old to
new.

Needs from phase 0: the updater fix, both in-container paths, the unit
alias, the variable rule, all in a **stable** release. After the first
stable exists, an old install reaches only stable releases through
`releases/latest`.

### Phase 4: the repositories

The owner renames both repositories. Before the rename:

- the two-identity `edge-update.sh` is on every edge host the project knows
  of, and a release note tells other operators to copy it again;
- no release is between its publication and its edge promotion;
- release-channels pull requests 6 and 7 are merged.

After it, one pull request changes the repository name wherever it is
written, and one changes the Go module path
([decision 1](#1-how-deep-the-rename-goes)).

Conditions that hold from then on: the old repository names are never
created again, the organisation is not renamed, old releases and tags are
not deleted, and the release signing key and the `channels` release stay in
the renamed repository.

### Phase 5: names inside sessions

New sessions get `cloudfield/run-` branches. MCP names change as
[decision 8](#8-names-people-see-inside-sessions) settles.

## The domain move

### What must exist on cloudfield.ai first

| Item | Why |
| --- | --- |
| `cloudfield.ai` serving the website | the site's canonical address |
| `edge.cloudfield.ai` and `auth.cloudfield.ai` resolving to the hosted edge | the edge obtains its own certificates, which needs the names to resolve to it first |
| A second GitHub OAuth app with the callback `https://auth.cloudfield.ai/signin/github/callback` | an OAuth app has one callback address (`internal/edge/service/oauth.go:34,53`). Edge accounts are keyed by GitHub user id (`:228-229`), so both apps sign the same person into the same account |
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

The change, one pull request:

- The edge takes an ordered list of address pairs. The first is primary.
- A request is served by the pair its host belongs to: sign-in pages,
  connector metadata, cookies, join links and the 421 text.
- A server's registration records the address it enrolled at. Every grant
  for that server names that address as issuer, whichever address the
  client came in on. Servers and clients therefore move independently.
- Connector tokens stay bound to the address they were issued on. A
  connector registered at `https://edge.onaether.dev/mcp` keeps working
  unchanged.
- One OAuth app per sign-in address.

The edge's key does not change, and a server pins that key per edge, not per
address (`internal/edge/agent/state.go:127`), so re-enrolling at the new
address keeps the pin.

### Order of the switch

Each step is reversible until step 6.

1. Create the DNS names, the OAuth app, the mailbox and its mail records.
2. Website: merge the aether-web rebrand with the site address set to
   `https://cloudfield.ai`, attach `cloudfield.ai` to the site, and check
   it. Then turn `onaether.dev` into a redirect to the same path on
   `cloudfield.ai`, and point `aethercloud.dev` at `cloudfield.ai` too.
3. Edge: deploy the multi-address release with only the old pair. Nothing
   changes.
4. Add the new pair second. Test: enroll a server at the new address, reach
   it from a CLI signed in at the old one, and use a connector at both.
5. Make the new pair primary. The old pair keeps serving.
6. Release with `https://edge.cloudfield.ai` as the default for **new**
   setups, `cloudfield-server edge move <address>` for existing servers,
   the new hostnames in the docs, and both listings resubmitted with the new
   connector address. `aether-server setup` writes the address into
   `/etc/aether/server.conf` (`cmd/aether-server/setup.go:363-368`), so an
   existing server keeps the old one until an admin moves it.

### How the old hostnames keep working

- `onaether.dev` stays registered and renewing with no end date. Whoever
  held it later could not forge grants, because servers pin the edge's key,
  but would receive device tokens and connector sign-ins from every install
  that still dials it.
- `edge.onaether.dev` and `auth.onaether.dev` stay served by the hosted
  edge. Retiring them cuts off every server that has not moved, every CLI
  link made there, every connector registered there and every printed join
  link. They are retired only if per-address counters on the edge
  (enrolled servers, device sign-ins, connector calls) read zero for a set
  period, and the recommendation is not to.
- `team@onaether.dev` keeps receiving.
- A self-hosted edge uses its operator's own addresses and is not affected.

## aether-web and the logo

- The site's rebrand continues in its draft pull request 48. It must set
  `SITE_URL`, `SITE_HOST` and `CONTACT_EMAIL` in `src/lib/seo.ts`, replace
  `public/aether-mark-*.png` and the icon and social images under
  `src/app/`, and reword `llms.txt` and `agents.txt`, which today tell
  crawlers "this is the Aether published at onaether.dev".
- The install command and the repository link (`src/lib/content.ts:3`,
  `src/lib/theme.ts:101`) change after phase 4. Until then they work
  unchanged, and after it through GitHub's redirect.
- `src/vendor/aether` is re-synced after phase 1 lands in the dashboard,
  and the site's dashboard screenshots are re-shot then.
- The Worker name `aether-web` and the package name are not shown to
  anyone and stay.

Logo hand-off. `aether-web/public/cloudfield/` is the source: `cloudfield-mark.svg`,
`cloudfield-logo.svg`, `cloudfield-logo-for-light-backgrounds.svg` and
`cloudfield-logo-small.svg`. Every icon this repository ships is generated
from `web/public/aether-mark.png` by `scripts/make-icons.py`
([CONTRIBUTING.md](../../CONTRIBUTING.md)): the dashboard's `web/public/icons/`,
the desktop's `desktop/build/icon.ico` and `desktop/build/icons/`, the
Android launcher `mipmap-*` and the Play feature graphic.

1. Copy `cloudfield-mark.svg` into `web/public/` and point
   `scripts/make-icons.py` at it. The script writes the wordmark
   (`FEATURE_WORDMARK`, `:100`) and the tagline into the Play graphic.
2. Run it and commit what it writes.
3. Redraw by hand what the script does not make: the Android notification
   icon `ic_stat_aether.xml` (traced from the mark), `docs/assets/aether-banner.svg`
   and `docs/media/aether-mark.png`.

The mark has to work in three places the old one was drawn for: as a
single-colour mask (the dashboard top bar and the Android notification
icon), inside a maskable icon's safe zone, and at 16 pixels. If it does
not, the fix goes back to the logo session, not into the script. The
dashboard, desktop and Android changes each get the design review loop.

## Sequencing with work in flight

### Release channels

[2026-11-15-release-channels.md](2026-11-15-release-channels.md) is pull
request 943 and its eleven implementation pull requests.

- Its new runtime names are neutral and stay: the `channels` release,
  `beta.json`, `stable.json`, `nightly.json`, `release_channel`. Their
  address contains the repository name and follows GitHub's redirect.
- It names one new secret, `AETHER_NIGHTLY_SIGNING_KEY`, which does not
  exist yet. Create it as `NIGHTLY_SIGNING_KEY`.
- Its pull requests 1, 2 and 6 rework `internal/selfupdate` and
  `release.yml`. The updater fix (pull request 2 here) lands after its 1
  and 2. Nothing here touches `release.yml`, because no release file is
  renamed.
- Its pull request 8 edits `scripts/edge-update.sh`. The two-identity
  change goes into that pull request or directly after it, so operators
  copy the script once.
- `v0.8.7`, the first stable, ships as Aether. Cloudfield starts with the
  `v0.9.0` line: phase 0 and phase 1 land as nightlies, and `v0.9.0` is the
  first stable that says Cloudfield. Phase 3 needs phase 0 in a stable
  release, so it ships in `v0.9.1` or later.
- Its pull requests 4 and 11 add dashboard copy and docs that say Aether.
  The phase 1 sweep runs after them, and the name check keeps later ones
  clean.
- The repository rename waits for its pull requests 6 and 7 and happens
  between releases.

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
  `cloudfield-internal` alias covers it with no change there.
- Its work item 10 rewrites install and update. The phase 3 installer
  change lands after it.

## Decisions for the owner

### 1. How deep the rename goes

| Option | What changes | Cost |
| --- | --- | --- |
| A. Brand only | Copy, docs, logos, app names, website, domain | Phases 1 and 2, plus the edge shim. People install Cloudfield and type `aether` |
| B. Brand, commands, variables | A, plus new command names, `CLOUDFIELD_*`, new unit name on new installs, both link schemes | All phases. About 24 pull requests. Old names stay as aliases |
| C. Everything | B, plus release files, images, data paths, app ids, wire names | Doubles every release, adds migrations of `/var/lib/aether` and the database with rollback risk, and makes a second Android and desktop app |

Recommendation: **B**, plus the Go module path as the last step. The module
path is source only and mechanical, but it touches 1,422 files, so it needs
a quiet hour on `main`. Not C: nothing in it is visible to a person.

### 2. Command names

| Option | Cost |
| --- | --- |
| `cloudfield`, `cloudfield-server`, `cloudfield-edge`, `cloudfield-internal` | Ten characters to type |
| A short name such as `cf` | `cf` is already the Cloud Foundry CLI and Cloudflare's CLI |
| Keep `aether` | Depth A |

Recommendation: the full names, no short alias shipped.

### 3. Old command names

| Option | Cost |
| --- | --- |
| Keep as aliases with no end date | A symlink per binary. Nothing to enforce |
| Remove at 1.0 | Breaks scripts, hook files in member homes and the unit of any host installed before the rename. Needs a migration command and a floor on downgrades |
| Remove after one release | Breaks every host that skips that release |

Recommendation: no end date for command names, in-container paths and the
variables the server sets inside sessions. `AETHER_*` variables an operator
sets stop being read at 1.0, with an error naming the replacement.

### 4. Spelling

Recommendation:

- "Cloudfield": one word, capital C only. Never "CloudField" or "Cloud
  Field". Lowercase `cloudfield` in commands, paths and addresses.
- "Cloudfield Edge" for the relay as a product and for the hosted service;
  "an edge" for one running relay, as the docs do today.
- "Enhanced", "Standard" and "Assistant" stay. "Ask Aether" becomes "Ask
  Cloudfield".
- Other parts are plain nouns after the name: Cloudfield server, Cloudfield
  dashboard, Cloudfield for Android, Cloudfield desktop app.
- Hostnames: `cloudfield.ai`, `edge.cloudfield.ai`, `auth.cloudfield.ai`.
  Contact: `team@cloudfield.ai`.

The alternative is "Edge" alone as a proper name, which reads as Microsoft
Edge in a sentence about browsers and sign-in.

### 5. Android application id

`io.aether.android` (`android/app/build.gradle.kts:50`). The Play listing is
unpublished; every install today is the signed APK from a release.

| Option | Cost |
| --- | --- |
| Keep `io.aether.android` | The id appears in the Play Store address and in Android's app details, for good |
| Change to `ai.cloudfield.android` before the first Play publication | A changed id is a different app. Every current install keeps the old app, which never updates again; each person installs the new one and links it again. The old app needs one last release that says so |

Recommendation: keep. The same goes for the desktop app id
`io.aether.desktop`: changing it makes a second app and drops taskbar pins
and notification permission. If the Play address matters, this is the only
cheap moment to change the Android id, and it must be decided before the
listing is published.

### 6. Release files and images

| Option | Cost |
| --- | --- |
| Keep `aether-*` release files and image names | The release page and `docker ps` show the old name |
| Publish release files under both names | 15 more files per release, with their verification time, with no end date: an old updater cannot learn the new names |
| Rename | Strands every old install; a renamed manifest does so silently |

Recommendation: keep. Revisit publishing both only if the release page
becomes something users read.

### 7. Repository names and the old domain

Recommendation: `cloudfield` and `cloudfield-web`, in the same organisation.
Never create a repository at an old name again. Keep `onaether.dev` and
`aethercloud.dev` registered with no end date, and keep the old edge
addresses served while the hosted edge exists.

### 8. Names people see inside sessions

- **Branch prefix.** `aether/run-…` is on every pull request a session
  opens. Options: keep it; or new sessions get `cloudfield/run-…` while both
  are accepted. Recommendation: change it for new sessions.
- **MCP names.** The connector's server name `aether` and 55 `aether_*`
  tools show in Claude and ChatGPT. Options: keep the tool names and change
  only the display title to Cloudfield; or rename to `cloudfield_*` for new
  sessions and on the new connector address only. Renaming costs every
  member's saved approvals for those tools, and four more characters on
  each of 55 names against a tool-description budget that is nearly full.
  Recommendation: change the title now, keep the tool names, and decide the
  rest after phase 3.

### 9. Micro VM names

Options: keep the `aether` names that stack introduces; or have it pick
`cloudfield` names before the first release that contains them, after which
they are fixed. Recommendation: keep. An operator sees only
`aether-vm-<key>.service`, next to an `aether.slice` that existing hosts
keep anyway.

### 10. Moving existing servers to the new edge address

Options: an admin runs `cloudfield-server edge move`; or a server whose
address is exactly the old default moves by itself when the edge tells it
the preferred address. Recommendation: the command first. Add the automatic
move in a later release if the counters show servers staying behind.

## To verify before building

Stated by the design and not yet tested:

- systemd accepts every `systemctl` verb the updater uses on a unit alias.
- GitHub requires a second OAuth app for a second callback host.
- A repository rename leaves the organisation's container packages and
  their Actions access unchanged.
- Electron names its data directory after `productName`.
- With release-channels pull request 7, a promotion called through
  `workflow_call` still carries `promote-edge.yml@refs/heads/main` as its
  identity.
- `promote-edge.yml` after the rename can verify an image attested before
  it.
- A server's bare repositories get the two-prefix hook when the server
  starts, not only when a repository is created.

## Implementation pull requests

Each is one session. Phase and dependencies are in brackets.

1. `docs(plans)`: this plan.
2. `fix(selfupdate)`: resolve companion binaries through symlinks and keep
   the release file name apart from the name on disk. `internal/selfupdate/update.go`,
   `internal/serverupdate/apply.go`, `serverupdate.go`, `trial.go`.
   [0; after release-channels 1 and 2]
3. `feat(server)`: answer to `cloudfield-internal`, mount both in-container
   paths, unit alias, own-unit lookup. `cmd/aether-server/main.go`,
   `internal/coordtransport/paths.go`, `internal/runtime/mounts.go`,
   `internal/serversetup/unit.go`, `internal/serverupdate/host.go`,
   `internal/localgw/update.go`, `packaging/systemd/aether-server.service`. [0]
4. `feat(envname)`: read `CLOUDFIELD_*` then `AETHER_*`; set both inside
   sessions. New `internal/envname`; `internal/cli/config.go`,
   `cmd/aether-server/main.go`, `cmd/aether-edge/options.go`, `main.go`,
   `metrics.go`, `data.go`, `internal/coordtransport/paths.go`,
   `internal/scheduler/scheduler.go`, `environment.go`,
   `internal/selfupdate/selfupdate.go`, `scripts/install.sh`,
   `install.ps1`, `install-sysbox.sh`, `desktop/main.js`,
   `android/.../Gateway.kt`, `UpdateCheck.kt`. [0]
5. `feat(edge)`: serve several address pairs; a grant's issuer is the
   address its server enrolled at; one OAuth app per sign-in address.
   `cmd/aether-edge/options.go`, `internal/edge/service/service.go`,
   `universal.go`, `pages.go`, `oauth.go`, `internal/edge/relay/control.go`,
   `connect.go`, `internal/edge/store`, `packaging/edge/`, `docs/edge.md`. [0]
6. `feat(edge)`: `edge-update.sh` accepts both signer identities, with a
   manual step that makes hosts stop until the script is copied again.
   `scripts/edge-update.sh`, `edge-update-test.sh`,
   `packaging/edge/upgrade.json`, `internal/edge/edgetest/testdata/`,
   `docs/edge.md`. [0; with or after release-channels 8]
7. `feat(gitengine)`: accept `cloudfield/run-` branches beside
   `aether/run-`. `internal/gitengine/slug.go`, `engine.go`,
   `transport.go`. [0]
8. `feat(desktop)`: register `cloudfield://`, fall back to the old data
   directory, find the app under both names. `desktop/electron-builder.yml`,
   `desktop/main.js`, `internal/localops/desktop.go`. [0]
9. `feat(android)`: register `cloudfield://`.
   `android/app/src/main/AndroidManifest.xml`, `DeepLink.kt`. [0]
10. `feat(dashboard)`: Cloudfield name, mark and icons. `web/src/` copy,
    `web/src/app/layout.tsx`, `manifest.ts`, `web/public/sw.js`,
    `web/public/` mark and `icons/`, `scripts/make-icons.py`, unit and
    end-to-end tests. Design review. [1; after release-channels 4]
11. `feat(cli)`: Cloudfield in CLI, server and edge messages, edge pages,
    assistant instructions, skill and hook text, connector title. Strings
    in `cmd/`, `internal/cli`, `scheduler`, `assistant`,
    `edge/service/templates`, `coordhooks`, `coordcli`, `ops`, `connector`,
    `mcpbridge`, and their tests. Not `internal/runtime/vm`,
    `internal/guest`, `internal/vmimage`. [1]
12. `feat(desktop)`: product name, dialogs and icons; Windows resources.
    `desktop/electron-builder.yml`, `package.json`, `main.js`,
    `desktop/build/`, `packaging/windows/versioninfo.json`,
    `scripts/update-smoke.ps1`. Design review. [1; after 8]
13. `feat(android)`: app name, strings, launcher and notification icons,
    Play listing. `android/app/src/main/res/`, `android/listing/`. Design
    review. [1]
14. `docs`: Cloudfield in `README.md`, `CONTRIBUTING.md`, `AGENTS.md`,
    `CLAUDE.md`, `AI_POLICY.md`, `docs/*.md`, `docs/legal/`,
    `docs/assets/`, `docs/media/`, `packaging/claude-connector/`,
    `packaging/chatgpt-plugin/` (names only). Not `docs/plans/`.
    [1; after release-channels 11]
15. `build`: fail `make public-audit` on the old name outside an allowlist.
    `scripts/public-audit.sh`, plus whatever the check still finds. [1; last]
16. aether-web: finish draft pull request 48 with the site address, contact
    and logo; re-sync `src/vendor/aether` after 10. [2]
17. `feat(edge)`: `edge.cloudfield.ai` as the default for new setups,
    `edge move`, new hostnames in docs and listings.
    `internal/edge/agent/agent.go`, `internal/edge/client/edgeclient.go`,
    `cmd/aether-server/edge.go`, `setup.go`, `scripts/install.sh`,
    `install-test.sh`, `docs/*.md`, `docs/legal/`, `packaging/`.
    [2; after the owner's steps 1 to 5]
18. `feat(cli)`: installers and the CLI create the new command names; new
    installs get `cloudfield-server.service`. `scripts/install.sh`,
    `install.ps1`, `cmd/aether/update.go`, `internal/macinstall`,
    `internal/serversetup/unit.go`, `cmd/aether-server/install.go`.
    [3; after 2, 3 and 4 are in a stable release, and after micro VM item 10]
19. `feat(server)`: prompts, role cards and hooks name
    `cloudfield-internal` in containers that have it. `internal/coordhooks/`,
    `internal/coordcli/`, `internal/harness/`,
    `internal/scheduler/acp_driver.go`. [3]
20. `docs`: new commands and `CLOUDFIELD_*` in docs, help and usage text,
    and the dashboard's copyable prompt; the old-to-new table. `docs/*.md`,
    usage strings in `cmd/`, `web/src/lib/config-prompt.ts`; the install
    command in aether-web. [3]
21. Owner: rename both repositories. [4]
22. `refactor`: the repository name where it is written.
    `internal/selfupdate/selfupdate.go`, `scripts/install.sh`,
    `install.ps1`, `android/.../UpdateTarget.kt`,
    `internal/serversetup/unit.go`, `internal/push/webpush.go`, docs links,
    `README.md` badges; `src/lib/theme.ts` in aether-web. [4]
23. `refactor`: Go module path, with `go.mod`, `Makefile`, `.golangci.yml`,
    `.github/workflows/ci-suite.yml`, `windows-install.yml`,
    `images/edge/Dockerfile`, `scripts/deploy.sh`, and regenerated
    `internal/guest/guestpb`. One scripted change in a quiet hour. [4]
24. `feat(gitengine)`: new sessions get `cloudfield/run-` branches and the
    `cloudfield` remote. `internal/gitengine/slug.go`,
    `internal/localops/pull.go`, `push.go`, `compare.go`,
    `cmd/aether/daemon.go`. [5; after 7 is in a stable release]
