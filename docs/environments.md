# Workspace environments

Every workspace runs its agents inside one image. This guide covers where that
image comes from: the prebuilt options, the coding-agent flows that build one
for you, verification, rollback, and changing the environment later. Each
member's home, login state, and installed user-local executables persist
separately in [environment-home.md](environment-home.md).

## Choosing an environment at creation

Workspace creation (admin) picks the starting image:

```sh
aether workspace init <name> --standard   # recommended
aether workspace init <name>              # minimal neutral image
aether workspace init <name> --image <ref>  # any admin-approved image
```

The **standard image** is published with each release
(`ghcr.io/3xdevops/aether-standard`) and carries git, go, node (via fnm),
python with uv, rust, and common build tools, so most projects work with
zero setup. The **neutral image** is a minimal Ubuntu with shell basics
only. The dashboard's create-workspace form offers the same three choices
with standard preselected; `server.info` reports both refs so clients can
name them.

A workspace keeps the exact ref it was created with across server
upgrades. Moving to a newer standard image is an explicit environment
change, never a silent one.

## Letting an agent build the environment

The dashboard's onboarding wizard adds an Environment step after workspace
creation. It needs the desktop app or `aether gui` (the flows run through
the local gateway on your machine) and one of the four setup-capable
agents installed locally: claude, codex, pi, or amp
(see [harnesses.md](harnesses.md)). Two agent paths:

- **Mirror my machine**: the agent inventories your local toolchains -
  language runtimes at your versions, version managers, dev CLIs, system
  libraries - and translates them to Ubuntu equivalents. It never reads
  dotfiles, credentials, or personal files.
- **From the repository**: the agent reads the project's own files
  (manifests, lockfiles, CI configs, and `.devcontainer/devcontainer.json`
  as the strongest signal) and derives what the project needs rather than
  what any machine has. The scan runs read-only: if the repository's git
  status changes during the run, the scan fails.

The agent runs headless with a fixed prompt and must produce exactly a
`Dockerfile` and a `manifest.json` naming each item with its version, the
Dockerfile lines that install it, and a check command. Raw agent output
streams behind a collapsed "View process" expander. Malformed output gets
one automatic retry; scans time out after ten minutes.

The same headless machinery runs one more scan, in the wizard's Agents
step: a `profile` scan reads what each coding agent on your machine is
configured with - paths and counts only, never file contents and never
anything the credential denylist or the secret scanner flagged - and
recommends which of it is worth importing into Aether, one sentence of
reasoning per harness. The recommendation is a checklist you edit and
approve; importing itself is profile sync, described in
[harnesses.md](harnesses.md).

You then review the result as a plain list: remove items, ask for changes
in your own words (the agent re-runs with your note), or approve. Approval
saves the definition and builds it in the background while onboarding
continues - runs use the creation-time image and show a "still building"
banner until the verified image swaps in. Every failure path offers
keeping the standard environment, so the wizard never dead-ends.

## Definitions, verification, and rollback

The server stores each workspace's environment as a versioned
**definition**: the Dockerfile, the manifest, which path produced it
(`mirror`, `repo`, `standard`, `manual`), and which harness wrote it.
Exactly one version is active, and the workspace image always equals the
active version's tag.

Builds run on the server's own Docker daemon. Built images are tagged
`aether/ws-<workspace-id>:<version>` and are local-only: never pulled from
a registry, rebuilt from the stored Dockerfile if the tag is missing.
The build context is the Dockerfile alone - `COPY` and `ADD` are rejected
at validation, so no server or local files can enter the image. After a
successful build the server boots a throwaway container and runs every
manifest item's check command; only an image whose claims all hold becomes
the workspace image. On any failure the workspace keeps its previous
image. Retention keeps the active and the previous version's tags and
removes older ones.

A build is arbitrary code on the server's Docker daemon, which is why
every call that saves, builds, edits, or rolls back a definition is
admin-guarded.

Administrators drive the lifecycle from the CLI:

```sh
aether env show       # active manifest, version history, statuses
aether env rebuild    # build the active (or --version <n>) definition
aether env rollback   # re-activate the previous good version
```

`aether env rebuild` follows the build to its terminal status and exits
nonzero on failure.

## Changing the environment later

The workspace page's Environment panel (admin) shows the active manifest,
the version history with failure details, and rollback. To change
something, pick a harness and describe the change in plain words - "add
the postgres 16 client", "bump go to 1.24". The server runs that harness
headless in a container with your registered login and tools (it must be
set up via `aether agent add` first), under the same output contract as
the wizard. The proposal appears as a Dockerfile diff plus the updated
item list; approving builds, verifies, and swaps exactly like any other
version, and a dismissed proposal simply stays in history unbuilt.

## Prebuilt images instead

`workspace init --image <ref>` accepts any registry reference the server
can pull. To prepare artifacts for administrator review and out-of-band
publication:

```sh
aether image init
aether image init --devcontainer
```

These create a normal `Dockerfile`, `.dockerignore`, and optionally a
`.devcontainer/devcontainer.json`; they do not build or push anything.
Aether never executes arbitrary project container metadata on the server.

## Protocol surface

For integrators: the control-channel methods are `env.save`, `env.build`,
`env.status`, `env.rollback`, `env.edit`, and `env.get`, all requiring
workspace admin. Build and edit progress ride the event feed as
`environment.build` and `environment.edit` events (version, status, output
line, failure detail). The local machine-side pieces - harness detection
and the scan socket - are documented in
[local-gateway.md](local-gateway.md).
