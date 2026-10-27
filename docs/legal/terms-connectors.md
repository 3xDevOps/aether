# Aether connector for ChatGPT and Claude: terms of service

> **DRAFT. Not published and not in effect.** The project owner and legal
> counsel must approve this text before it is hosted at the URL entered as
> `termsOfServiceURL` ([chatgpt-plugin.md](../chatgpt-plugin.md)) and linked
> from the Claude listing ([claude-connector.md](../claude-connector.md)).
> Bracketed items are decisions for the owner.

Effective: [date of publication]

These terms govern your use of the Aether connector at
`https://edge.onaether.dev/mcp` and the project edge it runs through
(`edge.onaether.dev`, `auth.onaether.dev`), operated by 3xDevOps
("we"). The Aether software itself is licensed under the GPL-3.0; these
terms do not change that licence.

## What the service is

The connector relays requests from ChatGPT or Claude to an Aether server you
are a member of, and returns the server's answers. It does not host agents,
run code, or hold provider credentials: every tool call runs on that server,
under its administrator's control and with your role there.

## Your account

You sign in with a GitHub account and are responsible for its security. You
may use the connector only with servers whose administrator invited you.
[Owner: minimum age, for example 13 or 16, and any eligibility rules.]

## Acceptable use

You will not use the connector to:

- reach a server, workspace or run you are not authorized to use;
- probe, overload, or bypass the edge's rate limits, sign-in or access
  checks;
- relay content or launch agent work that breaks the law, the
  [GitHub terms](https://docs.github.com/site-policy/github-terms/github-terms-of-service),
  or the policies of OpenAI or Anthropic.

We may suspend or block an account or server that does, as described in
[edge.md](../edge.md).

## Actions you take

Agents you launch or steer through the connector act with the access your
server gave them, and can push branches and open pull requests. ChatGPT and
Claude normally ask before a tool that changes anything, unless you chose to
always allow it; you are responsible for the actions you allow. No connector
tool merges pull requests, deletes repositories or workspaces, or changes
server settings, but an agent you instruct can do what its credentials
allow.

## Third parties

ChatGPT and Claude are provided by OpenAI and Anthropic under their own
terms. Your Aether server is operated by its administrator. We are not
responsible for either.

## Availability and changes

The service is provided as is, without warranty, and may change or stop at
any time. [Owner and counsel: limitation of liability, indemnity, governing
law and venue.]

We publish changes to these terms at this URL with a new effective date.

## Privacy

See the [connector privacy policy](privacy-connectors.md).

## Contact

[team@onaether.dev]
