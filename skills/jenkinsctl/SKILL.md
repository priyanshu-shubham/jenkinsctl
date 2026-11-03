---
name: jenkinsctl
description: Inspect, trigger and watch Jenkins jobs with the `jenkinsctl` CLI. Use when the user mentions Jenkins, a CI job or pipeline, a build number, or asks to run, re-run, check, or debug a build.
allowed-tools: Bash(jenkinsctl auth list:*), Bash(jenkinsctl jobs show:*), Bash(jenkinsctl runs list:*), Bash(jenkinsctl runs watch:*)
---

# Jenkins via `jenkinsctl`

`jenkinsctl` prints one compact JSON document on stdout per command.

## The one rule

If the output has no `error` field, the command succeeded. If it has one:

- `error.resume` present: run that exact command to keep waiting. Repeat until there is no error or the kind changes.
- otherwise: act on `error.kind`:

| kind | meaning / what to do |
|---|---|
| `run_failed` | The run finished with `.status` FAILURE/UNSTABLE/ABORTED. Read `.log_tail` (last 50 lines) and `.url`. |
| `timeout` | Still running or queued; always comes with `resume`. |
| `invalid_parameter` | Bad parameter name or choice; `error.valid` lists what is accepted. |
| `usage` | Wrong command or flags; the message says what to fix. |
| `auth` | Credentials rejected. Ask the user to run `jenkinsctl auth login` again. |
| `not_found` | Wrong job name, build number, or server path. |
| `queue_cancelled` | Someone cancelled the queued run. |
| `server_error` / `network` | Jenkins or the connection failed; retry once, then report. |

## Servers

Every Jenkins command needs `-s NAME`. List names with `jenkinsctl auth list`. Pick
the one the user means; ask if it is ambiguous. If there are none, ask the
user to run `jenkinsctl auth login` in their own terminal. It is interactive, so never
run it yourself.

## Commands

```sh
jenkinsctl jobs show  -s S JOB                      # parameters: name, type, default, choices
jenkinsctl runs list  -s S JOB [-n 20]              # recent runs, newest first
jenkinsctl runs submit -s S JOB [-p K=V]... [--watch]
jenkinsctl runs watch -s S JOB BUILD                # wait for / inspect one run
jenkinsctl runs watch -s S JOB --queue ID
```

JOB is the full name with `/` between folders (`team/api/main`) or a Jenkins
URL. A multibranch branch containing `/` is written `team/repo/feature%2Fx`.
BUILD is always a number. Take it from `runs list` or a previous result.

## Workflows

**Run a job:** `jenkinsctl runs submit` starts real CI work, which may include deploys.
Unless the user explicitly asked for this exact run, confirm the server, job
and parameters with them first. Run `jenkinsctl jobs show` beforehand so the
parameters are right, then:

```sh
jenkinsctl runs submit -s prod team/api/main -p BRANCH=feature/x --watch
```

Waits stop at 100s by default and return `resume`, which keeps every call
under the Bash tool limit. Do not raise `--timeout` above about 110s.

**Debug a failure:**
```sh
jenkinsctl runs list  -s prod team/api/main -n 10
jenkinsctl runs watch -s prod team/api/main 142                   # finished runs return immediately
jenkinsctl runs watch -s prod team/api/main 142 --log-tail 400    # need more context
```

Avoid `--logs`, which streams the entire console into your context. Prefer
`--log-tail N` with the smallest N that answers the question.
