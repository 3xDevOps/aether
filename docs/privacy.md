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
  dashboard's session storage, both private to the app.

No account, password, token, cookie or key is stored. There is no sign-in:
the phone's Tailscale login identifies it to your server
([networking.md](networking.md#the-dashboard)). Uninstalling the app deletes
everything above.

## What leaves the phone

- **To your server, and only there.** Every request the dashboard makes goes
  over HTTPS to the server whose name you typed, which you or your team run.
  That includes what you type into a terminal, the instructions you send an
  agent, file edits, approvals, and the run controls you use. The server keeps
  them as part of each run's record, on its own disk
  ([install.md](install.md#what-lives-in-the-data-directory)); who on your
  team can see them is in [teams.md](teams.md#roles) and
  [security.md](security.md#the-dashboard-gateways). The app refuses a plain
  `http://` address, forbids cleartext for the whole process, and refuses
  mixed content, so nothing travels unencrypted.
- **Your identity reaches the server through Tailscale, not through the
  app.** The server asks its own tailscaled which tailnet login owns the
  connecting device. The app sends no name, email, or identifier of its own.
- **To nobody else.** The app has no analytics, no crash reporting, no
  advertising, and no third-party library that talks to a network. WebView
  Safe Browsing is turned off in the app, so no visited URL, and no hash of
  one, is sent to Google. A link that leaves the dashboard opens in the
  phone's browser, under that browser's own policy.

## Permissions

`INTERNET`. Nothing else is declared or requested.

## Deleting your data

- On the phone: uninstall the app.
- On the server: the server's administrator owns the data directory and can
  delete a run, a member home, or the whole directory
  ([install.md](install.md#uninstalling)). The publisher holds no copy and
  cannot delete anything on your behalf.

## Children

The app is not directed at children and has no age-specific content or
features.

## Changes

Changes to this policy are commits to this file; its history is public at
<https://github.com/3xDevOps/Aether/commits/main/docs/privacy.md>.
