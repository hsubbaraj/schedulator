---
allowed-tools: Bash(git *)
description: Stage changes, auto-generate branch name and commit message using Haiku, then push
model: claude-haiku-4-5-20251001
argument-hint: [optional-branch-name]
---

# Ship command

1. Stage all changes: `!git add .`
2. Get the diff: `!git diff --staged`
3. Analyze the changes and generate:
   - A concise branch name (format: type/description, e.g., feat/add-login, fix/auth-bug)
   - A clear commit message (conventional commits format)
4. If the user provided $ARGUMENTS, use that as the branch name. Otherwise use the generated branch name.
5. Create the branch with the name
6. Commit with the generated message
7. Push to origin

Use the following format for the branch/commit:
- branch: type/short-description
- commit: type(scope): description
