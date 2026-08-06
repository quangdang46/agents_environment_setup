# Git Strategy for Multi-Agent Work

**Goal:** Understand how git works when multiple agents edit the same repo simultaneously.

---

## The Single-Branch Model

ACFS uses **one branch (`main`) with one worktree**. All agents commit directly to `main`.

This may surprise you if you're used to feature branches, but it's the right call when
dozens of agents work concurrently on the same repo.

---

## Why Not Branches or Worktrees?

Traditional git workflows assume humans working sequentially on isolated features.
Agent swarms break those assumptions:

**Branch-per-agent creates merge hell.** With 10+ agents making frequent commits,
merging N branches back to main produces cascading conflicts that waste more time
than they save.

**Worktrees add filesystem complexity.** Each worktree is a full checkout. With many
agents, disk usage multiplies and path confusion leads to cross-worktree edits that
corrupt state.

**Agents lose context across branches.** When an agent switches branches, its
in-context understanding of the codebase becomes stale. Single-branch means every
agent always sees the latest state.

**Logical conflicts survive textual merges.** Even when two changes don't conflict
at the text level, they can break semantics. A function signature change on one
branch and a new callsite on another will merge cleanly but fail to compile. On a
single branch, the second agent sees the signature change immediately and adapts.

---

## How Conflicts Are Prevented

Instead of branch isolation, ACFS uses three complementary mechanisms:

### 1. File Reservations (Agent Mail)

Before editing files, agents reserve them:

```bash
# Agent reserves files it plans to edit
file_reservation_paths(
    project_key="/data/projects/my-repo",
    agent_name="BlueLake",
    paths=["src/auth/*.rs"],
    ttl_seconds=3600,
    exclusive=true,
    reason="bd-42: refactor auth"
)
```

Other agents see the reservation and work on different files. Conflicts are
caught **before** edits happen, not after.

### 2. Pre-Commit Guard

The Agent Mail pre-commit hook checks reservations at commit time:

```bash
# Install the guard
mcp-agent-mail install-precommit-guard
```

If you try to commit a file reserved by another agent, the commit is blocked
with an explanation of who holds the reservation.

### 3. DCG (Destructive Command Guard)

DCG blocks dangerous git commands that could destroy other agents' work:

- `git reset --hard` -- would wipe uncommitted changes from all agents
- `git checkout -- .` -- same problem
- `git clean -fd` -- deletes untracked files other agents may need

See `onboard 10` for DCG details.

---

## The Recommended Workflow

```
1. Pull latest          git pull --rebase
2. Reserve files        file_reservation_paths(...)
3. Edit and test        rch exec -- cargo test / bun test / go test
4. Commit immediately   git add <files> && git commit
5. Push                 git push
6. Release reservation  release_file_reservations(...)
```

**Key principles:**

- **Commit early, commit often.** Small commits reduce the window for conflicts.
- **Push after every commit.** Unpushed commits are invisible to other agents.
- **Reserve before editing.** Don't touch files without a reservation.
- **Release when done.** Don't hold reservations longer than needed.

---

## What About Logical Conflicts?

The issue reporter correctly notes that avoiding textual merge conflicts doesn't
guarantee semantic correctness. ACFS addresses this with:

- **Frequent small commits** keep the delta small, reducing logical conflict surface
- **UBS scanning** (`ubs <changed-files>`) catches many semantic issues before commit
- **Compiler checks** (`rch exec -- cargo check`, `go vet`, `tsc`) run before every commit
- **Test suites** catch regressions immediately
- **Agent Mail threads** let agents coordinate on shared interfaces

For projects where this isn't sufficient, consider:
- Splitting the repo into smaller, focused crates/packages
- Using workspace-level dependency management (Cargo workspaces, npm workspaces)
- Defining clear module boundaries with stable interfaces

---

## Quick Reference

| Mechanism | What It Does |
|-----------|-------------|
| File reservations | Prevents two agents editing same files |
| Pre-commit guard | Blocks commits to reserved files |
| DCG | Blocks destructive git commands |
| `git pull --rebase` | Stays current with other agents' work |
| `main:master` push | Keeps legacy URLs working |

---

## AGENTS.md Sets the Rules

Each project's `AGENTS.md` file configures agent behavior, including:
- Branch policy (always `main`)
- Commit conventions
- File editing discipline
- How to handle unexpected changes from other agents

When you create a project with `acfs newproj`, this is set up automatically.

---

## Next

Learn the hands-on coordination loop for a real multi-agent task:

```bash
onboard 22
```

---

*The Agents Environment Setup - https://github.com/quangdang46/agents_environment_setup*
