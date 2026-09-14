# Data safety

The answers for **App content > Data safety**, derived from what the app
does (see [docs/privacy.md](../../docs/privacy.md) for the same facts in
prose). Play's form guide is at
<https://support.google.com/googleplay/android-developer/answer/10787469>.

The app itself sends nothing anywhere. The dashboard it shows is Aether's own
code, so under Play's rule that a webview counts when "your app is in control
of the code/behavior delivered through that webview", what the dashboard sends
to the member's server is declared as collected, even though the publisher
never receives it. Declaring it cannot be held against the app; leaving it out
can.

What the dashboard keeps on the phone itself - its appearance settings, the
last workspace and board grouping, the harness last launched per agent
account, dismissed update notices - is not declared: "User data accessed by
your app that is only processed locally on the user's device and not sent off
device does not need to be disclosed." [docs/privacy.md](../../docs/privacy.md)
lists it in full.

## Overview

| Question | Answer |
| --- | --- |
| Does your app collect or share any of the required user data types? | Yes |
| Is all of the user data collected by your app encrypted in transit? | Yes |
| Do you provide a way for users to request that their data is deleted? | Yes. Uninstalling removes everything on the phone; the server's administrator deletes server-side data. Link: `https://github.com/3xDevOps/Aether/blob/main/docs/privacy.md#deleting-your-data` |

## Data types

Every type below is **collected, not shared**, **required** (the app does not
work without it), **not processed ephemerally** (the server keeps run
records), and used for **App functionality** only. Nothing is used for
analytics, advertising, personalization, fraud prevention, or account
management.

| Category | Type | What it is |
| --- | --- | --- |
| App activity | Other user-generated content | What the member types into an agent's terminal and the instructions sent to a run. Play defines the type as "any other user-generated content not listed here, or in any other section"; terminal input and run instructions are content the member writes, not messages addressed to another person |
| App activity | Other actions | Approvals, run launches and the other controls the dashboard sends the server |
| App activity | App interactions | Whether you are online and which runs you have open, shown to your teammates (`aether who`) |
| Photos and videos | Photos | An image the member picks to paste into a terminal; on a phone the picker opens the gallery. It is the dashboard's only upload reachable from the phone: the onboarding profile import exists only on the local `aether gui` gateway |
| Files and docs | Files and docs | The contents of files edited in the dashboard's Files editor |

**Not shared** holds because of Play's user-initiated exemption,
"Transferring user data to a third party based on a specific user-initiated
action, where the user reasonably expects the data to be shared": the team or
employer running the server is a third party, but the member types that
server's name and every transmission above is something they typed, picked or
tapped.

Not collected, so leave unticked: Location, Personal info (the app sends no
name, email or ID; the server learns the tailnet login from Tailscale, not
from the app), Financial info, Health and fitness, Messages (the app has no
messaging feature; what a member writes goes to an agent, and is declared
above as user-generated content that their teammates can also see), Videos,
Audio, Calendar, Contacts, Web browsing, App info and performance (no crash
logs or diagnostics leave the phone), Device or other IDs.

## Other declarations on the same page

| Section | Answer |
| --- | --- |
| Ads | No, the app contains no ads |
| Advertising ID | Not used; the manifest declares no `com.google.android.gms.permission.AD_ID` |
| Target audience | 18 and over; not designed for children |
| Content rating questionnaire | Utility / productivity; no violence, no gambling. Users **can** communicate and share content with other users: shared live terminals, injected banners, coordination messages and approvals, all restricted to members an administrator invited to the server. Expect the "Users Interact" descriptor. Nothing is shared publicly |
| News app | No |
| Government app | No |
| Financial features | None |
| Health | None |
| Account deletion | Not applicable: the app creates no accounts |
