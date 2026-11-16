# Release Channels

**Goal:** an Aether install follows one of three release channels:
`nightly`, `beta` or `stable`. The newest green commit on `main` becomes a
nightly at most every 4 hours. An admin promotes a nightly to beta and a
beta to stable. One setting on the server picks the channel for the server
and the apps that connect to it.

**Status:** approved by the owner, 2026-11-15. The decisions dated
2026-11-14/15 are the owner's; the one on applying updates was settled
on 2026-11-16. Everything marked *Assumption* is this plan's choice and
stands unless the owner changes it.

## Decisions (2026-11-14/15)

- Three channels: `nightly`, `beta`, `stable`.
- Nightly builds the newest commit on `main` whose push CI is green, at
  most every 4 hours, plus a manual "build now". It is not one release per
  merge. The tag is `vX.Y.Z-nightly.N`. The base `X.Y.Z` is the last
  stable with the minor bumped: after `v0.8.7`, nightlies are
  `v0.9.0-nightly.N`. Promotion may override it.
- `main` is the only long-lived branch. Promotion tags an existing nightly
  commit `vX.Y.Z-beta.N`, and later `vX.Y.Z`. A hotfix lands on `main`
  first and is copied to a short-lived `release/X.Y` branch cut from the
  stable commit.
- "Newer" comes from a signed per-channel pointer and a build number in
  the signed manifest that only increases. The version string is a label.
- Promotion is manual. Nightly to beta needs the full suite green on that
  commit and an admin's click. Beta to stable is the maintainers' call,
  with a click. The days since the beta was cut are shown as a reminder
  only; nothing enforces a minimum. Auto-promotion is out of scope.
- One channel setting covers the server, the desktop app and the Android
  app. Changing it is super admin only. Desktop, Android and the CLI
  follow their server; with two servers, the one marked default.
- Aether Edge has the same three channels and keeps its own update
  mechanism and setting. The hosted Aether Edge service follows `stable`.
- No developer role. A Settings section holds the channel toggle, and
  switching it moves the server and the local app. Choosing nightly shows
  a warning.
- Leaving nightly: the server stays on its build until the lower channel
  catches up. Settings says so before the switch.
- The first stable is `v0.8.7`, promoted from a beta. Existing installs
  land on beta. New installs default to stable once it exists. Nightlies
  then start at `v0.9.0-nightly.1`.
- A server on any channel applies updates by itself when idle
  ([Applying updates](#applying-updates)). The project's own server runs
  nightly.
- Nightly assets are pruned after 14 days, except a nightly a waiting
  server may still run ([Pruning](#pruning)). Beta and stable are kept.

## Today

There is one channel.

- Every tag is a normal GitHub release, never a prerelease
  (`scripts/publish-release.sh:185`). `reconcile_latest`
  (`scripts/publish-release.sh:91`) marks the highest published release
  that carries `aether-release.json` as Latest, ordered by
  `scripts/android-version-code.sh`.
- `internal/selfupdate` finds the newest release by following GitHub's
  `/releases/latest` redirect (`LatestTag`,
  `internal/selfupdate/selfupdate.go:39`; `LatestManifestFrom`,
  `internal/selfupdate/manifest.go:217`), then downloads
  `aether-release.json` and `.sig` for that tag and checks the ed25519
  signature against `releaseKeys` (`internal/selfupdate/keys.go:13`). The
  manifest carries `version`, `commit` and `store_schema`
  (`internal/selfupdate/manifest.go:48-61`). It has no channel and no
  build number.
- `.github/workflows/release.yml` runs on a `v*` tag push (`:7-8`). It
  requires the pusher and re-runner to be repository admins (`:25-50`) and
  green push CI on `main` for the exact commit (`:92-101`,
  `scripts/release-ci-check.sh`). It builds 15 assets, writes
  `checksums.txt` and signs the manifest (`:929-959`), and keeps the
  release a draft until every asset is read back and verified
  (`scripts/publish-release.sh:176-191`). A run takes about 7 minutes and
  36 runner-minutes over 26 jobs (`v0.8.7-beta.14`).
- Releases run one per tag (`release.yml:20-22`). GitHub keeps one pending
  run per concurrency group and cancels older pending ones.
- Server updates are super-admin only
  ([workspace access](2026-11-10-workspace-access.md#who-may-do-what)).
  `internal/serverupdate` applies a scheduled update once the server is
  idle. Its preflight refuses a target older than the running version by
  semver unless `allow_downgrade` is set
  (`internal/serverupdate/preflight.go:112`), and always refuses a target
  whose `store_schema` is below the database's (`:120`).
- Roles are `viewer`, `collaborator`, `admin` and `super_admin`.

## Update consumers today

Every consumer starts from `/releases/latest` and the signed manifest of
that tag. Only Edge has anything like a channel. Android files are under
`android/app/src/main/kotlin/io/aether/android/`.

| Consumer | Finds the newest release | Trigger | Channel (this plan) |
| --- | --- | --- | --- |
| Server | `repository.Latest` → `LatestManifestFrom` (`internal/serverupdate/preflight.go:43-45`); background check every 6 hours (`internal/serverupdate/check.go:19`, `:125`), kept in `server-update/check.json` | `server.update` (`now`, `idle`, `cancel`) by a super admin (`internal/sshd/serverupdate.go:62`), or `aether-server update` as root (`cmd/aether-server/update.go:63`, `:200`) | Reads the pointer for its `release_channel` |
| CLI | `aether update`: `--version`, else the linked server's release when the server is ahead (`cmd/aether/update.go:85-93`, `internal/cli/update.go:16`), else `DefaultChecker` → Latest | the user | Follows its default server |
| Desktop app | `aether gui` runs `DefaultChecker` (`cmd/aether/gui.go:84`); `localgw.updateTarget` prefers the server's release (`internal/localgw/update.go:144-155`). The Electron shell has no updater (`desktop/main.js:285-289`); it is rebuilt after the CLI updates | the dashboard banner or the menu | Follows its default server, through the CLI |
| Android | Notice: the bundled CLI's `update --check --json` (`UpdateNotice.kt:175-198`), so Latest. Gate: the gateway's `update.check`, offering only the server's release (`GateCheck.kt:66-83`). The APK comes from the release assets, or the Play listing for Play installs (`UpdateTarget.kt:56-62`) | hourly check; the user installs | Follows its server |
| Edge | `docker pull ghcr.io/3xdevops/aether-edge:$channel`, `AETHER_EDGE_UPDATE_CHANNEL` `stable` or `latest` (`scripts/edge-update.sh:66`, `:496`); `:stable` needs a `promote-edge.yml` attestation (`:518-520`) and `release-check` against the signed manifest (`:522`) | systemd timer, daily | Its own setting, the same three names; the hosted service follows `stable` |
| Dashboard banner | Server row from `server.update_status` (`web/src/store/updates.ts:287-333`); client row from the gateway's `update.check` (`:246-273`); the `-32005` gate swaps in `UpdateGate` (`web/src/App.tsx:138-140`) | every 30 minutes and on focus (`web/src/components/update-checks.tsx:18-42`) | Shows the server's channel and the wait state |
| Install scripts | `/releases/latest`, falling back to the newest complete non-prerelease (`scripts/install.sh:510-520`, `:655-688`; `scripts/install.ps1:312`, `:695-723`); `AETHER_VERSION` pins a tag | the user | Unchanged: Latest is stable once it exists |

The server gates its clients: `min_client_version` is the server's own
release core (`internal/sshd/clientgate.go:48-51`), and an older client
gets `-32005` (`:56-70`). Clients never move past their server
(`internal/cli/update.go:16-38`). Both comparisons use semver
`selfupdate.Behind`.

## Versions and order

### Tags

| Channel | Tag | Built from |
| --- | --- | --- |
| nightly | `v0.9.0-nightly.N` | the newest `main` commit with green push CI |
| beta | `v0.9.0-beta.N` | a nightly's commit |
| stable | `v0.9.0` | a beta's commit, or a `release/0.9` commit for a hotfix (`v0.9.1`) |

`N` starts at 1 for each base. After an override promotes
`v0.9.0-nightly.12` to `v1.0.0-beta.1`, the next nightly is
`v1.0.0-nightly.1`: the base is the higher of the last stable with the
minor bumped and the core of the highest beta above it. *Assumption.*

### Why the version string cannot order updates

`selfupdate.Behind` compares by semver precedence
(`internal/selfupdate/check.go:76`, `:139`), and prerelease identifiers
compare as text (`:180-199`), so `beta` sorts below `nightly`.
`v0.9.0-beta.2`, promoted from `v0.9.0-nightly.12`, is below
`v0.9.0-nightly.10` while carrying more merges. The preflight
(`preflight.go:112`) and the client gate (`clientgate.go:56-70`) would
both get it wrong.

### Build number

Every manifest carries a signed `build`, a positive integer:

- Each nightly takes one above the highest signed build: the builds the
  three pointers name and the verified manifests of published nightlies
  whose tag claims a higher one. A tag a failed run left one above that
  is resumed; any other unpublished tag above it is refused. Pointers
  never move down and nightly tags are never deleted, so pruning or
  yanking a release never frees a number. Builds only increase along
  `main`.
- A beta or stable built from a nightly's commit carries that nightly's
  build. It is the same code, so it gets the same number.
- A hotfix on `release/X.Y` carries the build of the stable it fixes and
  a signed `hotfix` count, 1 for the first. `(build, hotfix)` orders it
  above that stable and below every later nightly.
- Releases from before channels have no `build` and count as `0`. Among
  them, semver decides, as today. Promoting one, which happens once, for
  `v0.8.7`, signs build 1.

Release A is newer than B when `(A.build, A.hotfix)` is higher. Equal
non-zero numbers are the same code under another label; two pre-channel
releases (build 0) fall back to semver. Every update check, the
preflight, the client gate, `MatchServer` and the dashboard banner use
this order. A binary records its build at build time. `Behind` takes
version strings and drops build metadata
(`internal/selfupdate/check.go:107-109`), so the build travels in its own
fields: `server.info` gains `build` and `min_client_build`, and
`update.check` and `server.update_status` gain the build of each release
they name. The client banner carries the client's build as semver build
metadata, `v0.9.0-beta.2+b.412`, which today's servers ignore.

Every place that compares versions today moves to build order:

- `internal/selfupdate/check.go:76` (`Behind`), `internal/cli/update.go:16-38`
  (`MatchServer`, `ServerFirst`), `internal/cli/link.go:211`
  (`UpgradeRequired`), `internal/sshd/clientgate.go:56-70`;
- `internal/serverupdate/preflight.go:112`, `serverupdate.go:280`, `:435`,
  `cmd/aether-server/update.go:204`;
- `internal/localgw/update.go:105`, `cmd/aether/update.go:297`;
- `cmd/aether-edge/releasecheck.go:71`, `:76`, `scripts/edge-update.sh:513`;
- `web/src/store/updates.ts:357`.

### Refusing a downgrade or a replayed pointer

- **Downgrade:** a server or client installs a release only when its
  `(build, hotfix)` is above the running one's, or equal while the
  channel changes (a relabel of the same code). The `store_schema` check
  (`preflight.go:120`) stays as a second, independent refusal.
  `allow_downgrade` stays a separate explicit action and still cannot
  cross a `store_schema`.
- **Replayed pointer:** each install keeps the highest `(build, hotfix)` it
  has accepted from each channel's pointer, in `server-update/check.json`
  on a server and `update.json` beside the client's `config.json` on a
  client. A state file that exists but cannot be read fails the check
  and is never overwritten. A pointer below
  that mark is refused with
  `channel pointer beta.json names build 380, below build 412 seen before`
  and the check counts as failed. An old pointer an attacker replays can
  therefore only fail checks; it cannot move an install back. A validly
  signed pointer with a very high build raises the mark for good; only a
  higher build recovers it.
- **No pointer moves down.** A pointer writer reads the current pointer
  and refuses to sign a lower build, so a lagging release listing or a
  deleted release cannot strand every install behind its high-water mark.
  A missing pointer is an error, not a first pointer: the writer signs
  the first one only when its caller says so (`first-pointer`). A run
  that fails mid-upload retries, and keeps its signed pair as a workflow
  artifact to restore from.
  Withdrawing a bad release means shipping a higher build: revert on
  `main` and "build now", or promote a newer nightly.
- **Channel confusion:** the pointer and the manifest both sign their
  `channel`. A client refuses a pointer whose channel is not the one it
  asked for, and a manifest whose channel its channel does not accept.
- **Withheld updates:** serving an old pointer forever stalls updates
  without failing them. The pointer signs `published_at`, and Settings →
  Updates shows its age, so a frozen channel is visible.

### Android `versionCode`

`scripts/android-version-code.sh` accepts only `alpha.N`, `beta.N` and
`rc.N` up to 199 (`:76-88`), and `release.yml` runs it on every tag
(`:97`), so today a nightly tag fails before anything builds. From
`v0.8.7`, build 1, the code comes from the build number, which
`release-build.yml` passes to the script because the tag does not carry
it:

```
100000000 + build * 100 + hotfix
```

That is above every code the current formula has produced
(`v0.8.7-beta.14` is `807314`) and leaves room for 20 million builds under
Android's limit of 2100000000. A nightly and the releases promoted from it
share a code; Android refuses only a lower code, so an equal one installs.
*Assumption*, to be checked on a device. Play needs a new code per upload,
which holds because only stables go to Play, and only a stable above
Play's newest code: a hotfix on an older line, or a stable that reuses a
build already on Play, skips Play.

## Manifest and channel pointers

### Signed manifest fields

`aether-release.json` gains:

```json
{
  "channel": "beta",
  "build": 412,
  "hotfix": 0,
  "version": "v0.9.0-beta.2"
}
```

`release-manifest` refuses a `channel` that does not match the tag's form
(`-nightly.N`, `-beta.N`, none). A client on stable accepts stable; on beta,
beta and stable; on nightly, all three. A manifest without `channel` is a
pre-channel release and is accepted on beta and nightly, which is how
existing installs reach the first channel-aware release.

### Pointers

GitHub has one Latest. Each channel gets a signed pointer, published as
assets of one fixed prerelease named `channels`:

```
https://github.com/3xDevOps/aether/releases/download/channels/beta.json
https://github.com/3xDevOps/aether/releases/download/channels/beta.json.sig
```

and the same for `stable.json` and `nightly.json`:

```json
{
  "schema": 1,
  "channel": "beta",
  "tag": "v0.9.0-beta.2",
  "commit": "<40 hex>",
  "build": 412,
  "hotfix": 0,
  "store_schema": 115,
  "published_at": "2026-11-20T10:00:00Z"
}
```

- A pointer names the newest release, by `(build, hotfix)`, among those
  its channel accepts.
- The client verifies the pointer, then fetches that tag's manifest as
  `fetchManifest` does today (`internal/selfupdate/manifest.go:229`), and
  requires the manifest's `version`, `commit`, `build` and `hotfix` to
  match the pointer.
- Asset download URLs have no API rate limit, like `/releases/latest`.
  Replacing an asset briefly answers 404; the client counts that as a
  failed check and retries with the existing backoff
  (`internal/selfupdate/check.go:359`).
- The `channels` release is a prerelease without `aether-release.json`, so
  `reconcile_latest` never picks it and installers never see it. Its tag
  does not match `v*`, so it starts no release.

From the first stable on, nightlies and betas are published as
prereleases, so GitHub's Latest is the newest stable: `reconcile_latest`
considers only non-prerelease releases (`publish-release.sh:98`). The
install scripts read Latest, so new installs get stable without a change
to them.

### Pointer writers

Each pointer is written by the run that publishes into it, with that
run's key, as the last step after publishing:

| Pointer | Written by | Key |
| --- | --- | --- |
| `nightly.json` | `nightly.yml` | nightly |
| `beta.json` | `promote.yml` to beta or stable, and `release.yml` for a hotfix, when the new release is above it | release |
| `stable.json` | `promote.yml` to stable, `release.yml` for a hotfix | release |

A nightly run never touches the release key, and no beta or stable
pointer waits on a nightly. `nightly.json` never names a beta or stable:
each was promoted from a nightly with the same build, and a hotfix's fix
is on `main` first, so a later nightly carries it. Writers share one
concurrency group, `channels`, with `cancel-in-progress: false`, and each
reads the pointer it replaces first. GitHub keeps one pending run per
group, so a pending nightly build and a pending Promote cancel each
other; the cancelled one is dispatched again.

### Trusted signer

Today the job that signs runs `release-manifest` with `go run` from the
released commit's checkout, with `AETHER_RELEASE_SIGNING_KEY` in its
environment (`release.yml:889`, `:929-959`), and publishes with that
commit's `publish-release.sh`. That has two problems for channels:

- Code merged to `main` runs holding a signing key, in every nightly and
  every Promote rebuild.
- Promoting a commit runs that commit's tooling. Promoting the
  `v0.8.7-beta.N` of the migration to `v0.8.7` would sign a manifest
  without `channel` and `build`, because that commit predates the new
  `release-manifest`.

So `release-build.yml` splits in two. Build jobs check out the commit,
build, compute `store_schema` and the edge section, and upload artifacts
with no key in reach. One sign-and-publish job checks out `main`, takes
only those artifacts and values, and runs `main`'s `release-manifest`,
`publish-release.sh` and pointer writer with the key. A promoted commit
then always gets the current manifest format.

### Keys

A nightly runs unattended, so its key should not sign stable.
*Assumption:*

- A second ed25519 key, `AETHER_NIGHTLY_SIGNING_KEY`, in a new `nightly`
  environment limited to `main`, signs nightly manifests and
  `nightly.json`.
- `AETHER_RELEASE_SIGNING_KEY` in the `release` environment signs beta
  and stable manifests and `beta.json` and `stable.json`.
- `internal/selfupdate/keys.go` gets a nightly key list. A nightly key
  verifies only a document whose `channel` is `nightly`, so a leaked
  nightly key cannot produce a beta or stable release.

[CONTRIBUTING.md](../../CONTRIBUTING.md#releases) asks for the `release`
environment to be limited to `v*` tags. Promote runs on `main`, so it must
also allow `main`. Branch rules cannot tell workflows apart, so any
workflow file on `main` could then name it; workflow files pass the same
merge gate as all code, and with the trusted signer no build step runs
where the key is.

## The channel setting

### Where it lives

`release_channel` is a server setting in the store. Only a super admin may
change it, the same rule as server updates
(`internal/sshd/serverupdate.go:62`). Every member sees the current
channel. A server that existed before channels gets `beta` from the
migration. A new server reads the pointers on first boot: `stable` when
`stable.json` verifies, `beta` once `stable.json` has answered 404 on two
checks in a row. Until then it has no channel, follows beta and applies
nothing by itself. The default cannot be compiled in, because the last
beta and the first stable are the same commit.

### Settings → Updates

A **Release channel** section with Stable, Beta and Nightly, beside the
existing update controls, read-only for anyone but a super admin.
Switching moves the server and the local app: the server saves the
channel, and the desktop app, Android app or CLI connected to it follows
on its next check.

Choosing Nightly asks first:

> **Switch to nightly builds?**
>
> Not recommended unless you are developing Aether.
>
> - Nightly builds come from Aether's main branch. Breaking changes
>   arrive as soon as they merge.
> - A nightly build may contain security issues that a later build fixes.
> - You cannot go back right away. This server stays on its nightly build
>   until the channel you return to catches up with it.
>
> [Cancel] [Switch to nightly]

Choosing a lower channel shows, before saving, when the running build is
above that channel's pointer:

> **Switch to beta?**
>
> This server runs `v0.9.0-nightly.12` (build 412). Beta is at
> `v0.9.0-beta.1` (build 380). The server stays on its current build until
> beta reaches build 412, then updates as usual. It never installs an
> older build.
>
> [Cancel] [Switch to beta]

`aether-server update --channel <stable|beta|nightly>` does the same from
the host, prints the same text, and needs `--yes`. It is a flag because
`aether-server update` takes no positional arguments
(`cmd/aether-server/update.go:60-62`). *Assumption:* the flag name.

### Applying updates

Decided 2026-11-16: on every channel a server applies updates by itself
when idle. When the check finds a release on the server's channel above
the running build, the server schedules it itself, exactly as
`server.update` `idle` would, and it applies once no session is working.
One function decides which channels do this, `selfApplies` in
`internal/serverupdate/autoupdate.go`.

- `AETHER_NO_UPDATE_CHECK=1`, which already turns the check off, turns
  this off with it.
- The server never schedules a build a super admin turned down. A
  `cancel`, an `allow_downgrade` update, and an update recorded failed
  after its 3 retries (`internal/serverupdate/apply.go:21`) each store
  that build as declined in the database, as does a self-scheduled
  update whose new binary failed its trial and rolled back, and a
  release the server failed to schedule 3 times. A cancel or an
  `allow_downgrade` update writes the mark first and is refused if the
  write fails; a failed or rolled-back update writes it afterwards. A
  cancel whose build cannot be read still cancels and says the release
  was not declined. The server schedules by itself only a build above the
  declined one, so a rollback holds until a newer build ships. A super
  admin's own `server.update` ignores the mark.
- Only a published release stamped with a build schedules by itself, and
  only a release with a build is scheduled: a source build is never
  replaced, and nothing changes until channel releases exist.
- A channel change retires a pending update the new channel does not
  accept, and `server.update` `idle` refuses such a release; `now` still
  installs it.
- The audit log names the server, not a member, as the actor of an update
  it scheduled.

### Apps follow their server

The CLI, the desktop app and the Android app have no channel setting.
They already update to exactly their server's release
(`internal/cli/update.go:16`, `internal/localgw/update.go:144-155`,
`GateCheck.kt:66-83`); this plan keeps that and adds:

- With several linked servers, the CLI and desktop app follow the default
  link: the top-level fields of the client's `config.json`
  (`internal/cli/config.go:40-45`). The desktop app can show another
  linked server, `gui_server` (`:43-46`). That server still gates the
  client: when it is above the client's build, its `-32005` gate offers
  its release, as today (`web/src/App.tsx:138-140`). When the client is
  above it, the dashboard shows the existing notice that the server is
  older (`web/src/store/updates.ts:347-360`). The default link alone
  decides what the client updates to on its own.
- With no linked server, a client follows `stable`, or `beta` until the
  first stable exists.
- Android's notice stops asking GitHub. Connected, it offers the server's
  release from `update.check`, as its gate already does; otherwise it reads
  the pointer for stable.
- Play installs get stable only: Play cannot carry a nightly every 4
  hours. A device on a beta or nightly server needs the release-signed APK
  from GitHub. *Assumption.*

A client on another channel than its server would break in both
directions: a newer client can call methods the server lacks, and an older
client is refused with `-32005` and has nothing on its own channel to move
to. Following the server rules both out.

### Edge

An Edge serves many servers, which can be on different channels, and
cannot read any one server's setting. So Edge has the same three channels
and keeps its own mechanism: an Edge host has its own setting,
`AETHER_EDGE_UPDATE_CHANNEL` in `/etc/aether-edge-update.env`
(`scripts/edge-update.sh:64-66`), now `nightly`, `beta` or `stable`,
default `stable`. Its compat floors (`internal/selfupdate/manifest.go:76-98`)
already let an older Edge serve newer servers.

The hosted Aether Edge service follows `stable`. *Assumption:* it is an
Edge host like any other, set to `stable` (the repository holds no
deployment config for it). Its `edge-update.sh` timer pulls
`aether-edge:stable`, which moves only when an admin runs
`promote-edge.yml` for a published stable release, and the host installs
it only with that run's attestation and a passing `release-check`.

- `:nightly` and `:beta` move when `nightly.yml` and Promote move their
  pointer; `:stable` moves by its own dispatch. `:latest` keeps following
  GitHub's Latest
  (`release.yml:983-987`), which becomes stable.
- `promote-edge.yml` gains `workflow_call` and a channel input. Promote
  calls it for `:beta` and `nightly.yml` for `:nightly`. The attestation's
  signing identity is the called workflow
  (`promote-edge.yml@refs/heads/main`), which hosts pin
  (`scripts/edge-update.sh:21`) and copy by hand, so they keep verifying.
  Each move publishes the `edge-promotion/v1` attestation naming the
  channel, which today is always `stable` (`promote-edge.yml:274`).
- `:stable` keeps its own step: an admin dispatches `promote-edge.yml`
  for the stable release, as today. Its 24-hour soak counts from that
  release's publication, which is when Promote ran, and
  `soak_override_reason` skips it. Promote to stable never waits on it.
  The compat floors let the older Edge serve updated servers meanwhile; a
  stable that raises them needs the override.
- `promote-edge.yml` reads today's release layout in four places, each of
  which changes:
  - Image attestations are signed by the workflow that ran the attest
    step. After the split that is `release-build.yml`, not `release.yml`
    (`promote-edge.yml:188`, `:254`). Pull request 6 accepts
    `release-build.yml` and, for releases built before it, `release.yml`.
    A release Promote or `nightly.yml` built has source ref
    `refs/heads/main`; a pushed tag keeps `refs/tags/<tag>`.
  - The qualifying run is looked up as a `release.yml` push run for the
    tag (`:140-145`), and its `edge-qualify` job by name (`:149`). A
    release Promote or `nightly.yml` built is found through that
    workflow's run for the commit, and a called job is named
    `<caller job> / edge-qualify`.
  - Order comes from `android-version-code.sh "$TAG"` (`:114`,
    `:219-226`), which cannot order channel releases; it moves to the
    manifests' `(build, hotfix)` from `release-check`.
  - The admin check (`:63-72`) and the soak (`:164-172`) apply to
    `:stable` dispatches only. A call is covered by its caller's rule:
    Promote's admin check, and for a scheduled nightly the green `main`
    commit. The `edge-promotion` environment (`:79`) must have no
    required reviewers, or every nightly waits on a person.
- A host that changes channel runs an image attested on its old channel,
  which `edge-update.sh:230-233` would refuse. It accepts the running
  image's attestation on any channel; the new image still needs one on
  the new channel.
- `latest` stays a channel name, following stable releases with no
  attestation, as today. It refuses a nightly manifest.
- `release-check` orders by the running release's signed manifest, bound
  to the running image digest, never by what the image reports. The
  updater keeps each verified manifest and signature on the host, so the
  order survives pruning.
- `min_upgrade_from` and a manual step's `since` are tags. With a running
  build they are ordered by the boundary tag's signed build; a boundary
  whose manifest cannot be read holds the update, unless its tag is below
  `v0.8.7`. They never name a nightly. A step for a change merged this
  cycle therefore names a beta that does not exist yet, and nightly hosts
  hold until it is published. Signing a build per boundary would remove
  that; it is a manifest format change this plan does not make.
- `release-check` reads the manifest's `channel` and `build` and refuses a
  channel the host's does not accept, or a lower build.

## Nightly builds

### Trigger

`nightly.yml` runs:

- on a schedule every 30 minutes, and
- on `workflow_dispatch` ("build now", admin only, which skips the
  4-hour wait).

Each run:

1. Takes `main`'s head and walks back to the newest commit whose push CI
   passes `scripts/release-ci-check.sh`.
2. Stops if that commit is already the newest nightly's.
3. Stops, unless dispatched, if the newest nightly was published less
   than 4 hours ago.
4. Builds and publishes it.

It runs in one concurrency group with `cancel-in-progress: false`, so one
build runs at a time and a build is never cancelled halfway. GitHub keeps
one pending run and cancels the older one, so a "build now" waiting
behind a running build can be replaced by a scheduled run. Step 3
therefore also skips the wait when the Actions API lists a
`workflow_dispatch` run of `nightly.yml` created after the newest
nightly was published.

At most 6 nightlies a day, about 3.6 runner-hours, and fewer on quiet
days. A merge is in a nightly within about 4.5 hours, or at once by
"build now".

### Tagging

A tag pushed with `GITHUB_TOKEN` starts no other workflow, so `nightly.yml`
cannot push a tag and let `release.yml` build it. The build half of
`release.yml` moves into a reusable workflow, `release-build.yml`
(`on: workflow_call`; inputs: tag, commit, channel, build, hotfix). Three
callers use it:

| Caller | Trigger | Who |
| --- | --- | --- |
| `release.yml` | `v*` tag push (hotfix) | admin pusher, as today |
| `nightly.yml` | schedule, or "build now" | the schedule: green `main` only; "build now": an admin |
| `promote.yml` | `workflow_dispatch` | an admin |

`nightly.yml` computes `N` and the build number and passes them to
`release-build.yml`. The sign-and-publish job creates the annotated tag
through the API once every asset is built, because `publish-release.sh`
creates the draft with `--verify-tag` (`:165-166`). A build that fails
earlier leaves no tag, and the next run reuses both numbers. A later run
that finds a nightly tag without a published release re-runs that
release instead of taking new numbers. The concurrency group makes them
race-free. `release.yml` refuses a pushed `-nightly.N` tag.

### Pruning

After 14 days, `nightly.yml` deletes a nightly's GitHub release and its
image tags. Its git tag stays, so the version still names a commit and
its build number stays taken. Betas and stables are kept.

A server that left nightly waits on its nightly, and its clients install
exactly that tag (`internal/cli/update.go:16-22`, `UpdateTarget.kt:59`),
so pruning it would leave new and gated clients a 404. Pruning therefore
keeps the nightly a waiting server runs: the newest nightly and every
nightly whose build is above `stable.json`'s, which covers a server
waiting for beta too, because beta is never below stable. It also keeps
a nightly whose image an Edge channel tag still carries. It prunes
nothing while `stable.json` or `nightly.json` cannot be read and
verified. Every nightly
since the last stable is therefore kept, so storage grows with the time
between stables. A server that stays on a nightly at or below
`stable.json` for 14 days, because its update check is off or its updates
keep failing, loses that nightly's assets, and its clients cannot install
it until the server updates. Accepted.

### Tests that read the previous tag

`TestEdgeUpgrade` builds "the previous release" from
`git describe --tags --abbrev=0 --match 'v[0-9]*' HEAD^`
(`internal/edge/edgetest/upgrade_test.go:97`). With nightly tags on
`main`, that becomes the previous nightly, and a hotfix tag on
`release/X.Y` is not reachable from `main` at all. The test must start from
the release `stable.json` and `beta.json` name, read from the pointers
rather than from `git describe`: the upgrades the other channels take.
Nightly builds add the previous nightly as a second starting point.

## Promote

`promote.yml` runs on `main` by `workflow_dispatch`:

| Input | Values |
| --- | --- |
| `from` | a nightly tag, a beta tag, or a full commit SHA of one |
| `to` | `beta` or `stable` |
| `version` | optional `X.Y.Z` override, at least the current nightly base |
| `first-pointer` | only for the migration's first stable |

It resolves the new tag, `vX.Y.Z-beta.N` (next `N` for that version) or
`vX.Y.Z`, and refuses:

- a dispatcher or re-runner who is not a repository admin (the same check
  as `release.yml:36-49`);
- `to: beta` from anything but a published nightly, or `to: stable` from
  anything but a published beta;
- a commit without green push CI for that exact commit, checked by
  `scripts/release-ci-check.sh` run from `main`, as `release.yml:84-101`
  does. A push run on `main` skips the code jobs when it reuses the merge
  queue's verdict for the same commit (`ci.yml:47-57`), and the merge queue
  runs every job, booting VMs only for changes that can affect them
  (`ci.yml:6-10`). Every commit on `main` passed the queue, so a green push
  run means the full suite passed on that tree. *Assumption:* that counts
  as "the full suite green". The stricter choice is to also require the
  daily scheduled run that runs every job (`ci.yml:15-16`) green on that
  commit;
- a build not above the target channel's current pointer. An equal
  build is the same code, and promoting it again would move the pointer
  without giving any install something to take.

Its run summary states the days since the beta was cut. It then calls `release-build.yml`, which rebuilds every asset
from that commit with the new version, runs the edge qualification and
the Defender scan again, and hands the artifacts to the trusted signer,
which signs with the release key, publishes, and moves `beta.json` and
`stable.json` as [Pointer writers](#pointer-writers) says.

Promotion rebuilds instead of copying, because binaries, the APK and the
Windows resources embed their version. The build number, commit, `go.sum`
and pinned toolchain are the nightly's. A rebuild of the same commit
publishes images only under its version tag, not again under the commit
tags (`release.yml:801-803`), which keep naming the first build of that
commit. *Assumption.*

An agent promotes by running
`gh workflow run promote.yml -f from=v0.9.0-nightly.12 -f to=beta` on an
admin's GitHub login. The admin check reads the dispatcher, so a
non-admin login is refused.

### Where the click is

Settings → Updates on a nightly or beta server shows a super admin the
newest beta with the days since it was cut, and the newest nightly, each
with a **Promote** button that opens the Promote workflow page on GitHub.
The click that promotes is the workflow's Run button. GitHub lets anyone
with write access press it; the workflow's admin check refuses everyone
but an admin. *Assumption.*

### Hotfix

1. Merge the fix to `main`. It ships in the next nightly.
2. Cut `release/0.9` from the line's promoted stable, the `v0.9.0`
   commit, if it does not exist, and cherry-pick the fix in a pull
   request to that branch. The 0.8 line's promoted stable is `v0.8.7`,
   so its first hotfix is `v0.8.8` on `release/0.8`.
3. When the branch's push CI is green, an admin pushes `v0.9.1` on its
   head. `release.yml` accepts a stable tag on the head of a
   `release/X.Y` branch cut from that line's promoted stable, and
   refuses every other commit off `main`. A tag runs the tagged commit's
   own workflow files with the release key, so the branch needs the
   protection `main` has before anyone tags it.
4. Beta can sit at a build above the hotfix yet below the fix's merge, and
   its pointer outranks `(build, hotfix)`. So once a nightly carries the
   fix, the admin promotes it to beta too.
   A hotfix that moves `beta.json` does not move the Edge `:beta` image:
   a run started by a tag cannot sign as `promote-edge.yml` on `main`,
   which hosts pin. `:beta` follows at that Promote.

`scripts/release-ci-check.sh` reads only push runs on `main` (`:31-32`), so
it gains a `release/X.Y` mode. CI's push trigger (`ci.yml:4-5`) adds
`release/*`, and `release/*` gets the same branch protection as `main`.

## Migration

Before step 1, the nightly key's public half is merged into
`internal/selfupdate/keys.go`, so that `v0.8.7-beta.N` and `v0.8.7` carry
it. An install without it refuses every nightly, and after step 3 only a
hotfix, or a beta and stable promoted from a later nightly, could deliver
it.

1. Ship the channel-aware updater as `v0.8.7-beta.N`, released the old
   way, by a tag on `main`. It becomes Latest and reaches every install
   through `/releases/latest`. Existing servers get `release_channel`
   `beta` from its migration. It should be the last release cut by a
   tag on `main`. Nothing refuses a later one, but such a release has no
   build: from step 3 no install with a build takes it, and it never
   becomes Latest.
2. Create the `channels` prerelease that holds the pointers. No pointer
   is published for the pre-channel beta: until a channel's first
   pointer verifies, installs on it read `/releases/latest`.
3. Promote it to stable `v0.8.7`, with `first-pointer` set: this run
   writes the first `stable.json` and `beta.json`. The trusted signer
   gives it `channel` and build 1 even though its commit predates them.
   It becomes Latest, so pre-channel installs that missed step 1 still
   update, and new installs get stable. A server on `v0.8.7-beta.N` has
   no build, so it does not install `v0.8.7` by itself: a super admin
   updates it once, and it applies updates by itself from then on.
4. Turn on `nightly.yml`: create the `nightly` environment with the
   nightly key, then set the repository variable `NIGHTLY_ENABLED` to
   `true` (`NIGHTLY_PRUNE_ENABLED` for pruning). Until then every job is
   skipped. The first nightly is `v0.9.0-nightly.1`, build 2.
5. Switch the project's own server to nightly.

From step 3, betas and nightlies are published as prereleases.
`reconcile_latest` orders by the manifests' `(build, hotfix)`, and by the
tag's `versionCode` only among releases from before channels.

## Implementation pull requests

Each is one session. 1 to 4 ship in the `v0.8.7-beta.N` of migration
step 1; 5 to 7 must merge before step 3.

1. `feat(selfupdate)`: `channel`, `build` and `hotfix` in the manifest;
   the build a binary records; build order at every site listed under
   [Build number](#build-number) except Edge; nightly key list.
2. `feat(selfupdate)`: pointer fetch and verification with the per-channel
   high-water mark; `Checker` reads the pointer for its channel, falling
   back to `/releases/latest` only until it has verified a pointer once,
   so a 404 while an asset is replaced never sends it back to Latest.
3. `feat(server)`: `release_channel` (migration, `beta` for existing
   servers, first-boot default from the pointers), super-admin check,
   `aether-server update --channel`, the wait state and builds in
   `server.update_status`, `build` and `min_client_build` in
   `server.info`, the build in the client banner, applying an update
   when idle ([Applying updates](#applying-updates)).
4. `feat(dashboard)`: the Release channel section, both confirmations,
   the wait state in the banner, the Promote links; Android's notice reads
   the server's release; clients follow the default link. Design review.
5. `build`: `android-version-code.sh` takes the build and the new
   formula; `release-manifest` writes `channel`, `build` and `hotfix`;
   `reconcile_latest` orders by the manifests' build.
6. `ci`: split `release.yml` into `release-build.yml` with the trusted
   signer job, no other behavior change; `promote-edge.yml` accepts its
   attestations and job names; the pointer writer script.
7. `ci`: `promote.yml`, calling `promote-edge.yml` through
   `workflow_call` for `:beta`; the `promote-edge.yml` changes under
   [Edge](#edge) other than pull request 6's.
8. `feat(edge)`: build order and channel in `release-check` and
   `edge-update.sh`, channel switches, the `nightly` and `beta` tags.
9. `ci`: `nightly.yml` with "build now", pruning, and the edge-qualify
   starting points; the signer job of `release-build.yml` takes its
   environment and key from the caller, and until then refuses a nightly
   channel or pointer.
10. `ci`: `release/X.Y` hotfix support in `release.yml`,
    `release-ci-check.sh` and `ci.yml`.
11. `docs`: `CONTRIBUTING.md#releases`, `docs/install.md` (Upgrading),
    `docs/edge.md` (including the promotion steps `promote-edge.yml:5-6`
    points to), `docs/security.md`.

Then the migration steps, run by an admin.
