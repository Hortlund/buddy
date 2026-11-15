---
name: code
description: Implement, run and verify a coding task in the appropriate project with Codex.
---

Carry the user's coding request through implementation and verification. Inspect the existing project, its instructions, working tree and relevant code before editing. Preserve unrelated user changes. Resolve routine implementation choices from the project and current request.

For repository work, start a Codex work assignment with the requested project and required installed capabilities. Include the user's acceptance criteria, relevant files and verification commands in the brief. Let Codex choose its tools and implementation steps. Use one assignment for a coherent change; use dependencies between assignments when work genuinely needs separate stages. Prefer the current Bud unless another Bud is requested or has a relevant role.

Use run_code for a small standalone example when running it is requested or needed to verify the answer. Use a work assignment when dependencies, network access, workspace writes, a service or several files are needed. Choose a compatible host using the advertised capabilities. The snippet tool's language list describes only the inline runner; project work can use the languages and toolchains installed on its host.

For an existing registered repository, use its project_id and required capabilities so Buddy can choose Buddy Home or a connected worker with a local checkout. Set workspace_mode to auto for isolated edits or checkout when the requested work includes pending changes or explicitly needs the original checkout. Preserve existing edits. A path on the Mac is not a path on the VPS. For a new project, create a directory in the assigned writable workspace and initialize or clone Git there as requested. Inspect project instructions and the current branch first. Report the actual checkout, branch, build and test results. Worker models and effort are chosen for each assignment from the executing host's live catalog unless fixed settings were selected.

Verify the resulting behavior using meaningful builds, tests or runtime checks. Inspect failures and fix those caused by the requested change. Report actual commands, results, changed files and remaining limitations. Keep commits, pushes, pull requests and other external changes within the user's authorized scope. Do not claim success from generated code or guessed output alone.
