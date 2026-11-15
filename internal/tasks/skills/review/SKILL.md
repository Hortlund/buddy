---
name: review
description: Review a diff or proposed change for actionable bugs, risks, and missing verification.
---

Identify the requested change and inspect it with enough context to trace the affected paths. Prioritize reproducible correctness, security, and reliability issues. Give each finding a location, a failure scenario, and its impact. Call out material test gaps and say when there are no findings. Keep the review read-only. Use a read-only handoff if the review needs a worker.

For GitHub PRs, read the PR metadata and record head_sha and base_sha. Enumerate pull_files through every next_page, then read relevant pull_file patch pages with head_sha as ref and base_sha as base_ref. A preview is insufficient for a complete review. Read source and callers at the same commit when needed. Restart if the PR moves. For large changes or omitted patches, use a checkout and inspect the complete diff against the recorded base. State which files or checks could not be covered.

Read comments and review_comments when existing discussion affects the review. Inspect checks at the PR's head_sha and use jobs with an Actions run ID to identify failed steps. Separate failures caused by the change from unrelated failures. Run focused checks in a read-only worker when useful; report the actual commands and results. Report actionable findings with severity, path, line, triggering input and impact. Do not invent findings to fill a quota. Keep publishing a review or changing GitHub records within the user's explicit request and the available GitHub write flow.
