# Privacy policy

Aether is self-hosted software. Nothing about you reaches the people who
publish it. This page is the privacy policy for the Aether Android app
(package `io.aether.android`), whether it came from a GitHub release or from
Google Play, and for the dashboard the app shows. Effective 2026-09-26.

## Who publishes it

Aether is published by 3xDevOps, <https://github.com/3xDevOps>. Questions
about this policy go to <https://github.com/3xDevOps/Aether/issues>.

## What the app stores on the phone

- **The server name you type on the first screen**, in the app's private
  storage. Nothing else the app writes is its own.
- **The WebView's ordinary cache** of the dashboard's files, and the
  dashboard's own local record of how you use it. That record holds how the
  dashboard looks (theme, sidebar width and whether it is collapsed, terminal
  font size, dock heights, diff wrapping, Cards/Map layout); where you were
  (the workspace you last opened, how the sidebar's run list is grouped, each
  workspace map's pan and zoom, whether you take control of a terminal when
  you open one); which harness you last launched for each of your agent
  accounts; which update notices you dismissed, by version; and
  the setup wizard's progress, which stays empty on a phone because that
  wizard only runs in the desktop `aether gui`. The dashboard writes these
  preferences in origin-local `aether.ui` storage: they are separate for each
  server origin, including its port. Nothing reads them but the dashboard,
  and they last until you uninstall the app. Both are private to the app.

No dashboard account, password, token, cookie or key is stored by the Android
shell. There is no dashboard sign-in: the phone's Tailscale login identifies
it to your server ([networking.md](networking.md#the-dashboard)). An app
login in the remote development browser is separate server-side state, as
described below. Uninstalling the app deletes everything stored on the phone.
None of it is backed up: the app opts out of Google's cloud backup and of
device-to-device transfer, so setting up a new phone asks for the server name
again.

## What leaves the phone

- **To your server, and only there.** Every request the dashboard makes goes
  over HTTPS to the server whose name you typed, which you or your team run.
  That includes what you type into a terminal, the instructions you send an
  agent, an image you pick from the phone to paste into a terminal, the
  contents of files you edit in the dashboard, approvals, and the run controls
  you use. The server keeps them as part of each run's record, on its own disk
  ([install.md](install.md#what-lives-in-the-data-directory)); who on your
  team can see them is in [teams.md](teams.md#roles) and
  [security.md](security.md#the-dashboard-gateways). The app refuses a plain
  `http://` address, forbids cleartext for the whole process, and refuses
  mixed content, so nothing travels unencrypted.
- **Your identity reaches the server through Tailscale, not through the
  app.** The server asks its own tailscaled which tailnet login owns the
  connecting device. The app sends no name, email, or identifier of its own.
- **Your server records you as a member on the app's first request.** From
  that answer it stores your tailnet login, which is an email address, and a
  display name taken from the part before the `@`, and writes one log line
  carrying that login and your device's Tailscale node ID. The record is how
  your administrator approves you and how your teammates see who did what
  ([teams.md](teams.md#roles)). It is kept by your own server, not by the
  publisher, and lasts until an administrator removes it.
- **Your teammates see when you are online and what you have open.** The
  dashboard reports to the server, every few seconds, that you are there and
  which run you are watching; the other members of that server see it
  ([teams.md](teams.md)). It is not kept as history.
- **To nobody else.** The app has no analytics, no crash reporting, no
  advertising, and no third-party library that talks to a network. WebView
  Safe Browsing is turned off in the app, so no visited URL, and no hash of
  one, is sent to Google. A link that leaves the dashboard opens in the
  phone's browser, under that browser's own policy.

Everything above goes to one server, the one you typed in, and stops there.
Nothing is sold, and nothing is handed to anyone the server's administrator
has not made a member of it.

## Remote-development data

The shared app browser runs on your server, not in the phone's WebView.
Browser input, app-terminal input, observations, frames, and explicit capture
requests travel through your Aether server. A page opened there makes its
own network requests from the run's network namespace; those app endpoints
and any third parties the page contacts receive the usual browser requests
under their own policies. This is separate from the Android shell's lack of
analytics or third-party reporting.

An app session can contain test-account cookies, tokens, personal data, page
URLs, or secrets printed by an app or terminal. Reading the live session,
streaming it, or taking a transient capture requires **Steer** and access to
the backing account, not merely permission to view a run. Deliberately retained
evidence copies instead use the existing evidence **View** permission and
expiry; review what you retain for that audience. Human members and the run
agent have distinct
per-surface control identities; taking over a shared app surface is not a
promise that the selected account's other processes or credentials are
isolated from that run.

The companion has no member-home or source-checkout mount. Its browser
context/profile is transient companion state, outside source control.
Resetting the browser context removes its pages and session state; a lost
browser process does not silently restore an authenticated session. Private
control sockets and lifecycle journals live below
`<data-dir>/scheduler/browser/`, not in the checkout or the run's visible
coordination files. See
[security.md](security.md#remote-development-and-browser-isolation).

Captures are explicit PNG files, not continuous recording. Each run can keep
at most **64 captures**, **128 MiB of PNG bytes in total**, and **8 MiB per
image**. Reaching either aggregate limit refuses a new capture with
`run capture limit reached; delete captures before taking another`; it does
not silently evict old evidence. Files and their metadata are stored below
`<data-dir>/coord/<run-id>/captures/`, exposed read-only to the run as
`/run/aether/captures/<random-id>.png`. Metadata identifies the run,
terminal or page, session/revision, capture time, dimensions, and page URL
where applicable. Optional Git HEAD and dirty-state fields describe only what
was actually observed at capture time; absent fields mean **unknown**, not a
clean checkout or the packet's later retained revision.

These captures remain transient run-owned files until explicit deletion or
cleanup of that run's coordination mount. A retained, explicitly closed TUI
run keeps its mount and captures until expiry or deletion. The live browser's
frame stream is not automatically archived. Browser diagnostics are bounded
observations, not a full traffic recording: the companion retains up to 100
console warning/error entries and 100 failed/error-response request entries
per page.
URLs and logged messages can still reveal sensitive information.

To preserve a reviewed capture before cleanup, explicitly select it in the
existing Evidence drawer, optionally enter verification notes, and choose
**Retain selected captures**. An agent with the advertised capability uses
`aether-internal artifact retain` with selected `artifact_ids`, optional
`verification_notes`, and an `idempotency_key`. This copies only the selected
PNGs into the existing retained evidence packet storage and returns a
`packet_id`; it does not verify the application or report an outcome.
Agents can pass that packet ID to the existing report `--evidence-ref`.
Retain before a success/failure report, capture deletion, or other cleanup
can remove the transient originals. A capture handle or live frame alone is
not durable evidence.

Retained copies remain readable after development processes stop, subject
to evidence access and expiry. Each run's retained copies are separately
bounded to **64 captures**, **128 MiB total**, and **8 MiB per PNG** across
packets; deleting transient captures does not reclaim retained-copy space.
Verification notes are bounded to **4096 UTF-8 bytes**. Original source,
capture time, session, geometry, URL and any observed Git boundary travel
with the copy. The packet's later retained Git revision does not rewrite or
attest to an earlier screenshot's Git boundary. Missing, truncated and
expired evidence is not a successful verification result.

Nothing is retained automatically. Resetting the browser does not delete
already-written PNGs, and retaining an image does not upload it to a public
pull request. There is no credential-entry recording. Do not capture
credentials or publish app logins, cookies, tokens, or customer data; Aether
cannot reliably redact secrets an app renders in pixels or text, or those
you include in verification notes. A member who explicitly downloads,
exports or publishes a capture creates a separate copy under that
destination's access and retention rules.

## Permissions

`INTERNET` is the only permission the app asks for. The androidx library
adds one signature-level permission the app defines for its own receivers,
which no other app can hold and which grants nothing.

## Deleting your data

- On the phone: uninstall the app.
- On the server: the server's administrator owns the data directory and can
  delete a run, a member home, or the whole directory
  ([install.md](install.md#uninstalling)). `aether member remove` destroys
  your environment terminal, deletes your member record and erases your
  member home. It refuses while anything still points at you - a run you
  launched, a schedule you own, or an agent profile you pushed - so an
  administrator deletes those first; what a run wrote stays in the data
  directory until it is deleted too ([teams.md](teams.md#roles)). The
  publisher holds no copy and cannot delete anything on your behalf.

## Children

The app is not directed at children and has no age-specific content or
features.

## Licence and open source notices

The app and the dashboard are under the GPL-3.0, and the libraries they use
are listed with their licences in [notices.md](notices.md). The app's first
screen links to both that page and the licence text.

## Changes

Changes to this policy are commits to this file; its history is public at
<https://github.com/3xDevOps/Aether/commits/main/docs/privacy.md>.
