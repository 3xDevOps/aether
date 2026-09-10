/**
 * The GitHub login the member runs in their environment terminal. The
 * signing scope is what lets `github.connect` register the key it
 * generates.
 *
 * It lives here rather than beside the screen that shows it because the
 * Playwright spec asserts the same string, and a Node process cannot import
 * the React module without its stylesheets.
 */
export const githubLoginCommand =
  'gh auth login --hostname github.com --git-protocol https --web --scopes admin:ssh_signing_key'

/**
 * What the dock actually types. The line goes in only once the gh probe has
 * answered, by which time the member can already have typed at the prompt,
 * so it opens with Ctrl-U to clear that prompt rather than land on top of
 * it. Ctrl-U kills backward from the cursor, so anything to the right of
 * the cursor survives and is appended; Ctrl-K would cover that, but the
 * environment terminal falls back to `/bin/sh` when an image has no bash,
 * and dash passes Ctrl-K through as input, which breaks the command
 * outright. A member sitting in an editor or a pager gets the byte as
 * input either way.
 */
export const typedLoginCommand = `\u0015${githubLoginCommand}`
