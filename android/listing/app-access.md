# App access

What to enter under **App content > App access** in the Play Console. Aether
is a client for a server the user runs themselves, reached over their own
private Tailscale network (a "tailnet") or through the Aether Edge, so a
reviewer needs one of the two to reach a dashboard. Play requires that access:
"If your entire app or parts of your app are restricted based on login
credentials, sign in details, memberships, location, or other forms of
authentication, you must provide all required details to enable access to your
app."
(<https://support.google.com/googleplay/android-developer/answer/9859455>.)

Choose **All or some functionality is restricted**, then **Add new
instructions** twice, in this order.

Provision the review tailnet before you submit: see
[README.md](README.md#before-you-submit). The account's sign-in details and
the server's real MagicDNS name are filled in by hand at submission; they are
never in the repository.

## Instruction set 1 - Review tailnet (required)

- Instruction name: `Review tailnet`
- Username: the review Tailscale account's email, filled in at submission
- Password: that account's password, filled in at submission
- Any other instructions, paste as is, replacing the server name on line 3:

```
Aether shows the dashboard of a server the user runs on their own private
Tailscale network. The account above is on a tailnet with a server running
for this review.

1. Install "Tailscale" from Google Play and sign in with the account
   above. Accept the VPN prompt.
2. Open Aether, choose "Tailscale", type the server name below and tap
   "Open dashboard".
   Server name: <the review server's MagicDNS name>
3. The dashboard opens already signed in as the review member. The board,
   a session's terminal and its diff are all reachable from the sidebar.

In this mode the app has no accounts, no sign-in and no password of its own:
the phone's Tailscale login identifies it, and the server decides what that
member may do. The server and the dashboard are open source, with
documentation at https://github.com/3xDevOps/Aether. In this mode the app
sends nothing to us and only ever loads the server named above, over HTTPS.
The other choice, "Aether Edge", signs in with GitHub through the Aether
edge and needs no tailnet; it is not needed to review the dashboard.
```

## Instruction set 2 - Without a server

The fallback if the review tailnet is unavailable. It exercises the setup
screen only.

- Instruction name: `Self-hosted server on a private network`
- Username: `none`
- Password: `none`
- Any other instructions, paste as is:

```
Aether is a client for a server the user runs themselves on their own
private Tailscale network (a "tailnet") or through the Aether Edge. In
Tailscale mode the app has no accounts, no sign-in and no password: the
phone's Tailscale login identifies it, and only devices on the same tailnet
can reach the server. Instruction set 1
gives an account on a tailnet with a server running for this review.

What the app does before a server is reached:

1. The first screen, "Connect to Aether", offers "Aether Edge" or
   "Tailscale". Choose "Tailscale": "Aether server" asks for a server name.
2. Type "example" and tap "Open dashboard". The app checks the name,
   opens the dashboard screen, and shows the WebView's own error page
   (net::ERR_NAME_NOT_RESOLVED) with a "Connection" button that
   returns to the first screen.
3. Type "http://example": refused with "The dashboard is served over
   HTTPS only", because the app never sends anything unencrypted.
4. Type "user:pw@example": refused, a server address carries no
   credentials.
5. Long-press the app icon on the launcher: the "Connection" shortcut
   opens the first screen again.
6. The privacy policy and licence links at the bottom of that screen
   open in the phone's browser.

The server and the dashboard the app shows are open source, with
screenshots and documentation at https://github.com/3xDevOps/Aether.
In Tailscale mode the app itself sends nothing to us and only ever loads the
server the user named, over HTTPS.
```
