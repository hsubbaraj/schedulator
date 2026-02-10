---
allowed-tools: Bash(git *)
description: Stage changes, auto-generate commit message using Haiku, then commit and push directly to main
model: claude-haiku-4-5-20251001
argument-hint: [optional-commit-message]
---

# Ship (direct) command

⚠️ This command commits **directly to the current branch (e.g., main)** and pushes to origin.
Use only for small, low-risk fixes.

1. Stage all changes:
   `!git add .`

2. Get the staged diff:
   `!git diff --staged`

3. Analyze the changes and generate:
   - A clear commit message in **Conventional Commits** format

4. If the user provided `$ARGUMENTS`, use that as the commit message.
   Otherwise, use the generated commit message.

5. Commit the changes with the commit message:
   `!git commit -m "<commit message>"`

6. Push directly to origin on the current branch:
   `!git push origin HEAD`

Use the following format for the commit:
- commit: `type(scope): description`
