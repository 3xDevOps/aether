# App access

What to enter under **App content > App access** in the Play Console. A
reviewer's phone is not on any tailnet, so the dashboard itself cannot be
reached from Google's side; the instructions say what can be exercised
without a server and why the rest cannot.

Choose **All or some functionality is restricted**, then **Add new
instructions**:

- Instruction name: `Self-hosted server on a private network`
- Username: `none`
- Password: `none`
- Any other instructions, paste as is:

```
Aether is a client for a server the user runs themselves on their own
private Tailscale network (a "tailnet"). The app has no accounts, no
sign-in and no password: the phone's Tailscale login identifies it, and
only devices on the same tailnet can reach the server. There is no public
server, so the dashboard cannot be opened from a device outside a user's
tailnet, including a review device.

What you can exercise without a server:

1. The first screen, "Aether server", asks for a server name.
2. Type "example" and tap "Open dashboard". The app checks the name,
   opens the dashboard screen, and shows the WebView's own error page
   (net::ERR_NAME_NOT_RESOLVED) with a "Server address" button that
   returns to the first screen.
3. Type "http://example": refused with "The dashboard is served over
   HTTPS only", because the app never sends anything unencrypted.
4. Type "user:pw@example": refused, a server address carries no
   credentials.
5. Long-press the app icon on the launcher: the "Server address"
   shortcut opens the same screen with the stored name filled in.
6. The privacy policy link at the bottom of that screen opens in the
   phone's browser.

The server and the dashboard the app shows are open source, with
screenshots and documentation at https://github.com/3xDevOps/Aether.
The app itself sends nothing to us and only ever loads the server the
user named, over HTTPS.
```

## If a review tailnet is set up

The only way a reviewer can see the dashboard is to be on a tailnet with a
running server. That means a Tailscale account created for the review, a
server on that tailnet with `web-port` set, and the account's sign-in details
in this form. It costs an always-on machine and an account someone at Google
holds; nothing in the repository provides it. If it is done, add a second
instruction set:

- Instruction name: `Review tailnet`
- Username and password: the review Tailscale account's
- Any other instructions:

```
1. Install "Tailscale" from Google Play and sign in with the account
   above. Accept the VPN prompt.
2. Open Aether, type the server name below and tap "Open dashboard".
   Server name: <the review server's MagicDNS name>
3. The dashboard opens already signed in as the review member. The
   board, a run's terminal, its diff and the approval inbox are all
   reachable from the sidebar.
```
