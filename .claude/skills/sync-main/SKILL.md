---
name: sync-main
description: Checkout main branch and sync it with remote origin/main. Use when user says "sync main", "update main branch", or "pull latest main"
allowed-tools: Bash(git *)
---

# Sync Main Branch

When the user wants to sync their main branch with the remote:

1. Check current branch status: `!git status`
2. Stash any uncommitted changes if needed: `!git stash`
3. Checkout main branch: `!git checkout main`
4. Fetch latest changes from remote: `!git fetch origin main`
5. Pull and merge remote changes: `!git pull origin main`
6. If there were stashed changes, ask user if they want to reapply them

Report to the user:
- Whether the local main was already up to date or what changed
- Number of commits pulled
- Any conflicts that need resolution
