---
name: report-failure-impact
description: Explain failed or incomplete checks in progress reports, handoffs, and release or merge decisions. Use when a reader needs to know what failed, the consequence, and whether it blocks their goal.
---

Lead with the impact. For each material failure, report three things:

- **What failed**: Name the command, check, or operation. Quote the shortest error that identifies the problem. Say if the run stopped early or the check never ran.
- **Consequence**: State which behavior, deliverable, or decision the failure affects, and what remains unverified.
- **Does it matter?**: Say **blocks**, **does not block**, or **impact unknown** for the current goal. Give the evidence and name any required gate that is still open.

Use the strongest evidence that is available and relevant: the current result, a targeted rerun, a comparable run on the base revision, or a required check on the current PR commit. A failure on the base shows that the base has a problem. Call it the same failure only when the symptoms and conditions match. Passing targeted checks can support a narrower conclusion even if the full run is incomplete. If the evidence is mixed, state the limit in plain language.
