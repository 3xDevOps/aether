# Privacy policy

Aether is self-hosted software. Nothing about you reaches the people who
publish it. This page is the privacy policy for the Aether Android app
(package `io.aether.android`), whether it came from a GitHub release or from
Google Play, and for the dashboard the app shows. Effective 2026-09-13.

## Who publishes it

Aether is published by 3xDevOps, <https://github.com/3xDevOps>. Questions
about this policy go to <https://github.com/3xDevOps/Aether/issues>.

## What the app stores on the phone

- **The server name you type on the first screen**, in the app's private
  storage. Nothing else the app writes is its own.
- **The WebView's ordinary cache** of the dashboard's files, and the
  dashboard's own local record of how you use it. That record holds how the
  dashboard looks (theme, sidebar width and whether it is collapsed, terminal
  font size, dock heights, diff wrapping); where you were (the workspace you
  last opened, how the board is grouped, whether you take control of a
  terminal when you open one); which harness you last launched for each of
  your agent accounts; which update notices you dismissed, by version; and
  the setup wizard's progress, which stays empty on a phone because that
  wizard only runs in the desktop `aether gui`. The dashboard writes it,
  nothing reads it but the dashboard, and it lasts until you uninstall the
  app. Both are private to the app.

No account, password, token, cookie or key is stored. There is no sign-in:
the phone's Tailscale login identifies it to your server
([networking.md](networking.md#the-dashboard)). Uninstalling the app deletes
everything above. None of it is backed up: the app opts out of Google's cloud
backup and of device-to-device transfer, so setting up a new phone asks for
the server name again.

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
  member home. It refuses while any run you launched still exists, so an
  administrator deletes those runs first; what they wrote stays in the data
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
