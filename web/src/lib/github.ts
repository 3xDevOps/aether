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
