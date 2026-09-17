# AI contributions policy

Aether is built with plenty of AI assistance. This policy exists because
low-quality, AI-generated contributions waste maintainer time.

## The standard

**You own what you submit.** Understand your code, test it, and be ready to
explain why it is correct and how it interacts with the rest of the system,
without re-prompting an LLM. This is what we expect of any contribution; AI
makes it easier to skip the work. Do not skip the work.

**Prove it works.** Verify the change end-to-end before submitting. "It
compiles" and "tests pass" are not enough.

- **Dashboard changes:** include a screenshot or screen recording of the
  change working in the pull request description, covering more than the
  happy path.
- **Server and CLI changes:** add tests for new behavior, preferring one that
  follows the real user path, and say in the pull request description what
  you tested and how.

Pull requests that clearly were not run or tested will be closed under this
policy.

**Disclose AI usage.** If an AI tool helped, add one line to the pull request
description:

```
This PR was made with the help of <name of tool>.
```

This helps reviewers calibrate. It does not count against the change.

**Do not list AI as an author.** No `Co-Authored-By:` trailer naming an AI
tool and no "Generated with" line, in any commit or pull request description.
Pull requests are squash-merged and the squash copies co-author trailers from
branch commits onto `main`, so remove them from every commit in the branch.
Most tools add these by default; turn that off in the tool's settings or
check `git log` before pushing.

**Prefer pull requests over AI-generated issues.** If AI helped you find a
bug, fix it and open a pull request. Do not paste the AI's output into an
issue. Unreviewed, AI-generated bug reports and security reports will be
closed without response.

**Do not submit unsolicited AI-generated reviews.** If you did not write the
code and are not a maintainer, do not point an LLM at someone else's pull
request and leave its output as a review comment.

## When a contribution does not meet this bar

- **First time:** we close the pull request or issue with a link to this
  policy and a brief explanation.
- **Two or more closures:** we block the account.

## Why we are not anti-AI

Aether is a tool for running coding agents, and the best contributions to it
often involve one. A contributor who uses an LLM to understand unfamiliar
code, draft a first pass, or catch edge cases they would miss is probably
more productive than one doing everything by hand. The difference is that
they are driving the AI, not the other way around.

If you are new to open-source contributing and want to learn, we are happy to
help: open an issue, ask questions, submit small pull requests.
