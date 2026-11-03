---
name: jenkinsctl
description: Inspect, trigger and watch Jenkins jobs with the `jenkinsctl` CLI. Use when the user mentions Jenkins, a CI job or pipeline, a build number, or asks to run, re-run, check, or debug a build.
allowed-tools: Bash(jenkinsctl auth list:*), Bash(jenkinsctl jobs show:*), Bash(jenkinsctl runs list:*), Bash(jenkinsctl runs watch:*), Bash(jenkinsctl runs logs:*)
---

# Jenkins via `jenkinsctl`

`jenkinsctl` prints one compact JSON document on stdout per command. The
exceptions are `runs logs`, which prints the log as plain text, and `api`,
which prints Jenkins' response body unchanged.

## The one rule

If the output has no `error` field, the command succeeded. If it has one, it
looks like this (fields after `message` appear only when relevant):

```json
{"error":{"kind":"invalid_parameter","message":"unknown parameter(s) [BRNCH] for team/api/main","valid":["BRANCH","ENV"]}}
```

`error` is either the whole output or added to a partial result (see
[Response shapes](#response-shapes)). It has `kind`, `message`, and optionally
`resume` (a command to continue), `valid` (accepted values) and `http_status`.

- `error.resume` present: run that exact command to keep waiting. Repeat until there is no error or the kind changes.
- otherwise: act on `error.kind`:

| kind | meaning / what to do |
|---|---|
| `run_failed` | The run finished with `.status` FAILURE/UNSTABLE/ABORTED. Read `.log_tail` (last 50 lines) and `.url`. |
| `timeout` | Still running or queued; always comes with `resume`. |
| `invalid_parameter` | Bad parameter name or choice; `error.valid` lists what is accepted. |
| `usage` | Wrong command or flags; the message says what to fix. |
| `auth` | Credentials rejected. Ask the user to run `jenkinsctl auth login` again. |
| `not_found` | The message says whether the job or the run is missing. For a missing job, `error.valid` lists the jobs that exist in the nearest folder. For a missing run, the message gives the latest run number. |
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
jenkinsctl runs logs  -s S JOB BUILD [--tail N]     # console log as plain text
jenkinsctl api -s S PATH [-X METHOD] [-d DATA]      # raw request; last resort, see below
```

JOB is the full name with `/` between folders (`team/api/main`), a Jenkins
URL, or a URL path (`team/job/api/job/main`). A multibranch branch containing `/` is written `team/repo/feature%2Fx`.
BUILD is always a number. Take it from `runs list` or a previous result.

## Response shapes

Optional fields are left out when they have no value.

**Run**: from `runs submit` and `runs watch`. `runs list` returns an array of these, without `log_tail`.

```json run
{
  "job": "team/api/main",
  "number": 142,
  "url": "https://ci.example.com/job/team/job/api/job/main/142/",
  "status": "FAILURE",
  "building": false,
  "started_at": "2026-11-03T10:15:00Z",
  "duration_ms": 84000,
  "estimated_duration_ms": 90000,
  "description": "nightly",
  "queue_id": 981,
  "parameters": {"BRANCH": "main"},
  "causes": [{"description": "Started by user Alice", "user_id": "alice"}],
  "log_tail": ["[ERROR] 2 tests failed", "Finished: FAILURE"]
}
```

- `status` is `RUNNING` while the run is going (`PENDING` for a moment at the
  start). After that it is the result: `SUCCESS`, `FAILURE`, `UNSTABLE`,
  `ABORTED` or `NOT_BUILT`.
- `duration_ms` is the time elapsed so far while the run is going.
- With `error.kind` set to `run_failed` or `timeout`, the Run object also has an `error` field.
- `log_tail` holds the last 50 log lines. It appears only when `runs watch` or
  `runs submit --watch` ends with a result other than `SUCCESS`.
- Optional fields: `estimated_duration_ms`, `description`, `queue_id`,
  `parameters`, `causes`, `log_tail`.

**Queued**: appears only with `error.kind: "timeout"` when the run never left the queue.

```json queued
{"job": "team/api/main", "status": "QUEUED", "queue_id": 981, "queue_url": "https://ci.example.com/queue/item/981/", "why": "Waiting for next available executor"}
```

**Job**: from `jobs show`.

```json job
{
  "full_name": "team/api/main",
  "url": "https://ci.example.com/job/team/job/api/job/main/",
  "buildable": true,
  "last_build": 142,
  "parameters": [
    {"name": "ENV", "type": "ChoiceParameterDefinition", "description": "Target environment", "default": "dev", "choices": ["dev", "prod"]}
  ]
}
```

- `parameters` is `[]` when the job takes none. `type` is the Jenkins class
  name, e.g. `StringParameterDefinition`, `BooleanParameterDefinition` (pass
  `-p FLAG=true`) or `ChoiceParameterDefinition` (the value must be one of `choices`).
- Optional fields: `last_build`, and per parameter `description`, `default`, `choices`.

**Servers**: from `auth list`. `insecure` appears only when TLS verification is off for that server.

```json servers
[{"name": "prod", "url": "https://ci.example.com", "user": "alice", "insecure": true}]
```

## Workflows

**Run a job:** `jenkinsctl runs submit` starts real CI work, which may include deploys.
Unless the user explicitly asked for this exact run, confirm the server, job
and parameters with them first. Run `jenkinsctl jobs show` beforehand so the
parameters are right, then:

```sh
jenkinsctl runs submit -s prod team/api/main -p BRANCH=feature/x --watch
```

Waits stop at 100s by default and return `resume`, which keeps every call
under the Bash tool limit. Do not raise `--timeout` above about 110s. For
long-running jobs, keep running `resume` until the run finishes.

**Debug a failure:**
```sh
jenkinsctl runs list  -s prod team/api/main -n 10
jenkinsctl runs watch -s prod team/api/main 142             # result plus the last 50 log lines; finished runs return immediately
jenkinsctl runs logs  -s prod team/api/main 142 --tail 400  # more of the log when 50 lines aren't enough
```

`runs logs` exits at once with the log so far, even while the run is going. A
note on stderr says whether the run is still going or how it finished. Always
pass `--tail` with the smallest N that answers the question, because a full
log can be tens of thousands of lines. Never use `--follow`: it blocks until
the run ends, so use `runs watch` to wait instead.

**Anything else (raw requests):** use `jenkinsctl api` only when no command
above fits, for example to abort a run or cancel a queued one. It can do
anything the user's Jenkins account can, including deleting jobs, so tell the
user exactly what a non-GET request will do before you run it.

```sh
jenkinsctl api -s prod -X POST /job/team/job/api/job/main/142/stop   # abort run 142
jenkinsctl api -s prod -X POST /queue/cancelItem -d id=981           # cancel queue item 981
jenkinsctl api -s prod '/computer/api/json?tree=computer[displayName,offline]'
```

GET responses are printed unchanged. Responses with no body come back as
`{"http_status":302,"location":"..."}`. Errors use the usual `error` object.
