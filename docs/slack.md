# Slack

A Slack bot inside `aether-server` sends you a direct message when a run needs
you and lets you answer from Slack, for example on your phone. It connects out
to Slack over Socket Mode, so it works on a tailnet-only server with no edge
and no public URL.

## Set it up

An admin does this once, on the server.

1. Open <https://api.slack.com/apps>, choose **Create New App**, **From a
   manifest**, pick the Slack workspace, paste this YAML and choose **Create**:

   ```yaml
   display_information:
     name: Aether
     description: Tells you when a run needs you, and lets you answer it.
   features:
     bot_user:
       display_name: Aether
       always_online: true
     app_home:
       home_tab_enabled: false
       messages_tab_enabled: true
       messages_tab_read_only_enabled: false
   oauth_config:
     scopes:
       bot:
         - chat:write
         - im:history
   settings:
     event_subscriptions:
       bot_events:
         - message.im
     interactivity:
       is_enabled: true
     org_deploy_enabled: false
     socket_mode_enabled: true
     token_rotation_enabled: false
   ```

2. **Basic Information**, **App-Level Tokens**, **Generate Token and Scopes**:
   any name, add the scope `connections:write`, **Generate**. Copy the
   `xapp-...` token.
3. **Install App**, **Install to \<workspace\>**, **Allow**. Copy the Bot User
   OAuth Token, `xoxb-...`.
4. On the server:

   ```sh
   sudo aether-server slack setup
   ```

   It asks for the app-level token, then the bot token, on standard input.
   Typing at a terminal hides them; never pass tokens as arguments. Piped
   input is one token per line: `sudo aether-server slack setup < tokens.txt`.
   It checks both tokens with Slack, writes them to
   `<data-dir>/slack/app-token` and `<data-dir>/slack/bot-token` (mode 0600,
   directory 0700; the data directory defaults to `/var/lib/aether`), and
   prints the bot name and Slack workspace. Apply it with
   `sudo systemctl restart aether-server`. A server that runs as its own user
   (`User=aether` in the unit) must be able to read the files, so run the
   setup as that user: `sudo -u aether aether-server slack setup`.

The app asks for three scopes and nothing else:

| Scope | Why |
| --- | --- |
| `chat:write` | Sends and edits the bot's messages |
| `im:history` | Delivers what you write to the bot (the `message.im` event) |
| `connections:write` (app-level token) | Opens the Socket Mode connection |

The bot reads no channel, posts only in its direct messages, and looks up no
one.

## Link your Slack account

Each member links their own account.

```sh
aether slack link
```

It prints a one-use code like `ABCD-EFGH` that expires in 10 minutes. In
Slack, open the bot under **Apps** and send the code in its **Messages** tab,
in any case, dash optional. The bot answers `Linked to <your Aether name> on
<server>.` A new code replaces your unspent one. A Slack account links to one
member at a time; linking it again moves it.

**Settings → Notifications → Slack** in the dashboard does the same with
**Link Slack** and **Unlink**. `aether slack` shows whether the server is
connected to Slack, and your link.

A link acts as you, judged by the device you ran `aether slack link` on, like a
[connector](connectors.md). It stops working when you unlink, that device is
revoked, your member is removed, or the server runs under `edge-access
approved-devices` and that device is not approved. A link made over a member
SSH key or a tailnet connection has no device.

## What it sends

Exactly what web push sends ([the table of needs and lines](networking.md#notifications)),
to the same member, when the run starts needing them, and to every admin when
a workspace's spend reaches its budget's warning or cap. The wait is the same:
while you use a dashboard, nothing is sent until you have left every dashboard
alone for a minute. A budget notice is a new direct message with a link to the
workspace's budget.

Each run gets one direct message with:

- the run's title and the need line;
- a link to the run in the dashboard, when the server serves one (`web-port`);
- buttons, when the need can be answered there.

The message is edited in place as the need changes and says
`Nothing needs you now.` once it stops. Slack does not notify for an edit, so
each new need also arrives as a reply in that message's thread. When Slack does not take a message, the server tries again
every minute while the run still needs you. After you unlink, or when a run
changes hands, a message still showing a need is edited to say only that it
no longer follows the run.

## What you can do

**Reply in a run's thread.** The reply goes to the run as a message from you,
shown as `<member> via Slack` in the dashboard. The usual rules apply: to your
own run that no one controls, or to a run you control, it reaches the agent at
once; otherwise it waits about 45 seconds for the member in control to approve
it.
The bot answers in the thread with what happened, or the server's error.

**Press a button.** Each answers once:

- an Enhanced run's permission request: its allow-once and reject-once
  options. There is no "always" option, so Slack cannot change what the agent
  may do next time;
- an Enhanced run's question with options: one button per option;
- a Default run's `sudo` command, in either mode: **Allow once** and **Deny**
  ([security.md](security.md#sudo-in-a-default-run)).

The bot answers in the thread, for example `Answered: Reject.` or
`Already answered, or no longer pending.`

The message links to the dashboard instead of showing buttons for:

- a request longer than 600 characters (Slack would show it cut, and you would
  decide text you did not see);
- a question that is a form or asks you to open a link;
- more than 25 options, or an option label longer than 75 characters;
- a Standard run's own request, which is answered in its terminal.

Your role still decides: a viewer cannot answer. You cannot launch runs or run
other commands, and the bot posts in no channel. A top-level direct message
to the bot, outside a thread, only links.

## Limits

- 30 actions (replies and buttons) at once, then one more every 2 seconds.
  Past that the bot answers `your Slack link <Slack user id> used its 30
  actions and gets one more every 2s; retry in <seconds>`.
- Agent text is cut: run title 120 characters, need line 240, request text
  600. It is escaped, so an agent cannot mention people (`@channel`) or
  disguise links.
- One Slack workspace per server: the one the app is installed in.

## Turn it off

- A member: `aether slack unlink`, or **Unlink** in **Settings**.
- An admin: `sudo aether-server slack remove` deletes both token files, then
  `sudo systemctl restart aether-server`.

Messages already sent stay in Slack ([privacy.md](privacy.md#slack)).

## Troubleshooting

| You see | Fix |
| --- | --- |
| `aether slack link` fails with `slack.link: Slack is not set up on this server; an admin sets it up with: sudo aether-server slack setup (docs/slack.md)` | Ask an admin to set it up |
| `aether slack` shows `not connected: slack: auth.test: invalid_auth`, or `apps.connections.open` with `invalid_auth` or `not_authed` | A token was revoked or mistyped. Run `sudo aether-server slack setup` again |
| `aether slack` shows `not connected:` with a network error | The server cannot reach slack.com. It honours `HTTPS_PROXY` from `/etc/aether/aether-server.env` |
| The bot answers `your Slack link no longer works (...); run aether slack link again` | The device or member behind the link is gone; link again |
| The bot answers `That code is wrong, expired or already used. Run aether slack link for a new one.` | Run `aether slack link` again |
| Messages stop arriving | Run `aether slack` and check your link; see the list of what stops a link in [Link your Slack account](#link-your-slack-account) |
