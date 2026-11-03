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
`runs list`, `runs watch`, `runs logs`), so Claude Code still asks before it
runs `runs submit` or `api`.

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
| `jenkinsctl runs logs -s S JOB BUILD [--tail N] [--follow]` | Console log as plain text |
| `jenkinsctl jobs show -s S JOB` | Job details and the parameters `submit` accepts |
| `jenkinsctl api -s S PATH [-X METHOD] [-d DATA]` | Raw authenticated request for anything else, e.g. `-X POST /job/team/job/api/142/stop` |

`runs watch` and `runs submit --watch` stop waiting after `--timeout`
(default `100s`, which keeps each call under Claude Code's 2-minute Bash limit)
and return a `resume` command. Use `--timeout 0` to wait as long as it takes.
A run that doesn't succeed includes its last 50 log lines.

For logs, use `jenkinsctl runs logs -s S JOB BUILD`. It always prints plain text:

```sh
jenkinsctl runs logs -s prod team/api/main 142 --tail 200   # last 200 lines
jenkinsctl runs logs -s prod team/api/main 142 > run.log    # whole log to a file
jenkinsctl runs logs -s prod team/api/main 142 -n 50 -f     # last 50 lines, then keep following until the run ends
```

Without `--follow` it prints the log so far and exits, even if the run is
still going. `--follow` has no time limit, and Ctrl-C stops it cleanly
(exit code 130, no error report).

`JOB` is the full name (`team/api/main`), a Jenkins URL inside the job, or a
URL path (`team/job/api/job/main`). A
multibranch branch containing `/` is written `team/repo/feature%2Fx`. `BUILD`
is always a number.

## Output contract

stdout is one JSON document per command, compact unless stdout is a terminal.
The exceptions are `runs logs` (plain text) and `api` (the response body as
Jenkins sent it).
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
