---
name: debug
description: Reproduce a software failure, isolate its cause, and verify a focused fix.
---

Identify the expected behavior and the smallest reproducible failure. Trace the relevant inputs and execution path before proposing a change. Separate observations from hypotheses. For substantial repository work, hand off a brief with the repro, scope, and verification criteria. Apply a focused fix when the user asks for one, and report which checks ran and what remains uncertain.

Use run_code for a self-contained reproduction in an advertised language. For repository builds, dependencies, network access or several files, use a Codex work assignment on a host with the required capabilities and project checkout. Capture the failing command and output, make the requested fix, and rerun the check. Never substitute predicted output for execution evidence. If a runtime or dependency is missing, report the observed failure and use another compatible host when one is available.
