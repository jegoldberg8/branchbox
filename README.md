# branchbox

Run any git branch of any project in its own dev container, with tmux inside
and a jcode that joins your host's mesh. Two branches of the same service can
run side by side against one shared set of databases.

```bash
branchbox infra up myproject          # shared databases, once
branchbox up feature/a worker         # branch A
branchbox up feature/b worker         # branch B
branchbox ps
```

Nothing is written into the projects you run. Profiles, generated
devcontainer configs, logs and state all live outside the target repository.

## How it works

A branch is resolved in the order you would expect: an existing checkout, then a
local branch, then `origin/<branch>` (fetching first, in case your remote-tracking
refs are stale), and finally a new branch started from `base`. Creating a branch is
reported explicitly, so a mistyped name is visible rather than silently becoming a
new branch.

Each stack is a git worktree of the target repository, bind-mounted into a
container built from one shared base image. The base image carries git, tmux,
gh, direnv, ripgrep, mise and jcode; the project's *toolchain* comes from the
target repo's own `mise.toml` at container start, so one image serves Go, Node
and Rust projects without a rebuild.

Services run as tmux windows inside the container, so a stack is something you
work in: attach, read the output, restart one service after an edit, leave the
rest running. Output is also tee'd to a host directory, so `branchbox logs`
works without attaching.

For watching several services at once, `branchbox compose <branch>` runs the
same commands under process-compose and attaches its TUI: status, restarts and
per-process logs on one screen. It generates the config from the profile, so the
commands are identical to the tmux runner's, and it takes over any tmux window
for a service it manages rather than running it twice. `--detach` starts the
project without attaching, and a later `branchbox compose <branch>` reattaches
rather than restarting. `--stop` ends the project and leaves the container up.

A profile may declare shared backing services as a Docker Compose file. Those
start once per profile and every branch container of that profile joins the
same network and reaches them by name. Databases and Temporal are shared across
branches deliberately: that is what makes a side-by-side comparison of two
branches meaningful.

## Commands

| Command | Purpose |
| --- | --- |
| `branchbox up [project] <branch> [service ...]` | provision the worktree, start the container, run services |
| `branchbox run [project] <branch> <service ...>` | start more services in a running stack |
| `branchbox shell [branch] [window]` | attach to the stack's tmux session |
| `branchbox compose [project] <branch> [service ...]` | attached process-compose TUI over the stack's services (`--detach`, `--stop`) |
| `branchbox exec [project] <branch> <command ...>` | run one command inside a stack |
| `branchbox ps [project]` | list stacks and the shared infra they depend on |
| `branchbox logs [project] <branch> [service] [--follow]` | read a service's log from the host |
| `branchbox down [project] <branch> \| --all` | remove containers; worktrees and logs are kept |
| `branchbox infra <up\|down\|status> [project] [--purge]` | manage a profile's shared services |
| `branchbox image build` | rebuild the base image |
| `branchbox profiles` | list known profiles |

Inside a project's checkout the project argument can be omitted: branchbox
matches the working directory against each profile's repository.

## Profiles

A profile describes one project. The minimum is three lines:

```toml
name = "example"
repo = "~/code/example"

[services]
dev = "make dev"
```

The full schema:

| Key | Meaning |
| --- | --- |
| `repo` | the main checkout; branch containers run from worktrees of it |
| `worktrees` | where worktrees are created (default `<repo>-worktrees`) |
| `base` | ref a brand-new branch starts from (default: the remote's default branch) |
| `secrets` | globs of gitignored local files symlinked from the main checkout into each worktree |
| `[infra] compose` | Docker Compose file of shared backing services, resolved relative to the profile |
| `[env]` | environment for every container of this profile |
| `[ports]` | `NAME = { base, stride }`; each stack takes one slot, published on the host |
| `[services]` | name to command, run in a tmux window |
| `[setup] steps` | commands run once when a container starts |

Profiles are searched in `~/.local/state/branchbox/profiles` first, then in this
repository's `profiles/`. A user profile shadows a shipped one of the same name.

**Your own profiles belong in `~/.local/state/branchbox/profiles/`**, not in this
repository: a profile names your local paths, your project's internal service
names and its local fixture credentials. `profiles/example.toml` and
`infra/example.compose.yaml` here are templates; copy them out and edit the
copies. The repository ignores everything else under `profiles/` and `infra/`,
so a profile kept alongside them will not be committed by accident.

Ports are allocated as a *set*: `slot = hash(project, branch) % 16`, probed
forward until every port of the set is free. The same branch therefore keeps
the same ports across restarts, and two live stacks never interleave.

## The jcode mesh

`~/.jcode` is bind-mounted into every container, and the host jcode server's
socket is relayed into it, so a jcode started inside a branch container attaches
to the same server as the one on your host and can DM or broadcast to the rest.

The relay exists because the server's socket lives under `/var/folders` on
macOS, which Docker Desktop refuses to bind-mount. `branchbox` re-execs itself
as a small unix-socket proxy to bridge it into the already-shared jcode home.
The container's jcode is pinned to the host's version at image build time,
since the two must speak the same protocol.

## Requirements

- Docker (branchbox starts Docker Desktop for you on macOS)
- `devcontainer` CLI: `npm i -g @devcontainers/cli`
- `git`

## Notes on the target repository

- Worktrees live outside the repository and the repository itself is never
  modified. `git status` in your checkout is identical before and after.
- The worktree and the main checkout are both mounted at their *host* paths. A
  worktree's `.git` is a pointer file holding an absolute path into the main
  repository's `.git/worktrees`, so git cannot resolve HEAD if they are mounted
  anywhere else. `/workspace` is a symlink to the branch checkout for
  convenience.
- Each branch gets its own build-cache volumes, so branches do not serialize on
  one cache or thrash on differing dependency sets.

## Install

```bash
go build -o branchbox . && ln -sfn "$PWD/branchbox" ~/.local/bin/branchbox
```

The binary finds its assets (image, profiles) by walking up from its own
resolved path, so a symlink into `~/.local/bin` works. `BRANCHBOX_HOME`
overrides the lookup and `BRANCHBOX_STATE` relocates the state directory.
