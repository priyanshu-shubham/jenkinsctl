# jenkinsctl: a Jenkins CLI for humans and agents

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/priyanshu-shubham/jenkinsctl/main/install.sh | bash
```

This installs the latest release binary into `~/.local/bin` (Linux and macOS,
amd64 or arm64; the checksum is verified first) and the Claude Code skill into
`~/.claude/skills/jenkinsctl`. Then add a server with `jenkinsctl auth login`.

Options go after `bash -s --`:

```sh
curl -fsSL https://raw.githubusercontent.com/priyanshu-shubham/jenkinsctl/main/install.sh | bash -s -- --project .  # skill in this repo's .claude/skills, shared with the team
curl -fsSL https://raw.githubusercontent.com/priyanshu-shubham/jenkinsctl/main/install.sh | bash -s -- --version v0.1.0
curl -fsSL https://raw.githubusercontent.com/priyanshu-shubham/jenkinsctl/main/install.sh | bash -s -- --uninstall
```

`BIN_DIR` changes the binary location. Windows users can use WSL or the zip
from [Releases](https://github.com/priyanshu-shubham/jenkinsctl/releases). With
Go installed, `go install github.com/priyanshu-shubham/jenkinsctl/cmd/jenkinsctl@latest`
works too, but it skips the skill.

The skill pre-approves only the read-only commands (`auth list`, `jobs show`,
`runs list`, `runs watch`), so Claude Code still asks before it runs `runs submit`.

## Servers

```sh
jenkinsctl auth login              # prompts: name, URL, username, API token (hidden); verified before saving
jenkinsctl auth list               # saved servers, no network
jenkinsctl auth logout -s staging
```

Profiles live in `~/.config/jenkinsctl/config.json` (mode 0600; override the path with
`JENKINSCTL_CONFIG`). There is no default server: every command that talks to Jenkins
takes `-s NAME`.

## Commands

| Command | What it does |
|---|---|
| `jenkinsctl runs list -s S JOB [-n 20]` | Recent runs, newest first, with parameters and causes |
| `jenkinsctl runs submit -s S JOB [-p K=V]...` | Validate parameters, start a run, print it once it has a build number |
| `jenkinsctl runs submit … --watch` | …and wait for it to finish |
| `jenkinsctl runs watch -s S JOB BUILD` | Wait for run BUILD to finish (returns at once if already finished) |
| `jenkinsctl runs watch -s S JOB --queue ID` | Same, starting from a queue item |
| `jenkinsctl jobs show -s S JOB` | Job details and the parameters `submit` accepts |

Waiting commands also take:

- `--timeout D` defaults to `100s`, which keeps each call under Claude Code's 2-minute Bash limit. Use `0` to wait with no limit.
- `--log-tail N` puts the last N console lines in the result. Failed runs get 50 lines by default.
- `--logs` streams the whole console. It goes to stdout in text mode and to stderr in JSON mode.

`JOB` is the full name (`team/api/main`) or a Jenkins URL inside the job. A
multibranch branch containing `/` is written `team/repo/feature%2Fx`. `BUILD`
is always a number.

## Output contract

stdout is one JSON document per command, compact unless stdout is a terminal.
Force a format with `-o json|text`. The exit code is 0 on success and 1 otherwise.

On failure, an `error` object is added to whatever partial result exists:

```json
{"job":"team/api","number":142,"status":"RUNNING",...,
 "error":{"kind":"timeout","message":"team/api #142 still RUNNING after 1m40s",
          "resume":"jenkinsctl runs watch -s prod team/api 142 --timeout 1m40s"}}
```

| `error.kind` | Meaning |
|---|---|
| `run_failed` | Run finished FAILURE/UNSTABLE/ABORTED/NOT_BUILT. The run and its `log_tail` are included. |
| `timeout` | Still queued or running. Run `error.resume` to keep waiting. |
| `invalid_parameter` | Unknown parameter or choice. `error.valid` lists the accepted ones. |
| `usage` | Bad command, flag, or server name |
| `auth` | Credentials rejected (401/403) |
| `not_found` | Job, run or queue item doesn't exist |
| `queue_cancelled` | Queued run was cancelled |
| `server_error` / `network` / `error` | Jenkins, connection or local failure |

In text mode the error, the valid values and the resume command go to stderr.

## Development

```sh
go test ./...
scripts/build-release.sh v0.0.0-dev   # the same archives the release workflow publishes, written to dist/
```

Pushing a `v*` tag runs `.github/workflows/release.yml`, which tests, builds and publishes a GitHub release.

The tests run the whole CLI against an in-process fake Jenkins (`internal/cli/cli_test.go`).
The agent-facing guide is the skill at [`skills/jenkinsctl/SKILL.md`](skills/jenkinsctl/SKILL.md).
