package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/priyanshu-shubham/jenkinsctl/internal/jenkins"
)

const jobHelp = `JOB is the job's full name with folders separated by "/" (e.g. team/api/main),
any Jenkins URL inside the job, or a URL path like team/job/api/job/main.
Multibranch branches containing "/" are encoded by Jenkins, so write
team/repo/feature%2Fx.`

func (a *app) runsCmd() *cobra.Command {
	return group(&cobra.Command{
		Use:   "runs",
		Short: "List, submit, watch and read the logs of job runs",
	}, a.runsListCmd(), a.runsSubmitCmd(), a.runsWatchCmd(), a.runsLogsCmd())
}

func (a *app) runsListCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:     "list -s SERVER JOB",
		Short:   "List the most recent runs of a job, newest first",
		Long:    "List the most recent runs of a job, newest first, with parameters and causes.\n\n" + jobHelp,
		Example: "  jenkinsctl runs list -s prod team/api/main --limit 5",
		Args:    exactArgs("JOB"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 1 {
				return usageErr("--limit must be at least 1")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			builds, err := c.ListBuilds(cmd.Context(), args[0], limit)
			if err != nil {
				return a.explainNotFound(cmd.Context(), c, args[0], 0, err)
			}
			return a.emit(builds, func(w io.Writer) { buildsTable(w, builds) })
		},
	}
	a.serverFlag(cmd)
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "number of runs to show")
	return cmd
}

func (a *app) runsSubmitCmd() *cobra.Command {
	var (
		params []string
		wo     watchOpts
	)
	cmd := &cobra.Command{
		Use:   "submit -s SERVER JOB [-p KEY=VALUE]...",
		Short: "Start a new run and print it once it has a build number",
		Long: `Start a new run of JOB. Returns once Jenkins assigns a build number.
With --watch, keeps waiting until the run finishes; a result other than
SUCCESS is reported as error kind "run_failed" with the last 50 log lines.

Parameter names and choice values are checked against the job definition
first, because Jenkins silently ignores unknown parameters. Unset parameters
use the job's defaults. See 'jenkinsctl jobs show' for what a job accepts.

If --timeout expires first, the error has kind "timeout" and a "resume"
command that continues waiting.

` + jobHelp,
		Example: `  jenkinsctl runs submit -s prod team/api/main -p BRANCH=feature/x
  jenkinsctl runs submit -s prod team/api/main -p ENV=staging --watch --timeout 10m`,
		Args: exactArgs("JOB"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := wo.check(); err != nil {
				return err
			}
			values, err := parseParams(params)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			c, err := a.client()
			if err != nil {
				return err
			}
			job, err := c.GetJob(ctx, args[0])
			if err != nil {
				return a.explainNotFound(ctx, c, args[0], 0, err)
			}
			if job.IsFolder() {
				return usageErr("%s is a folder, not a job", job.FullName)
			}
			if !job.Buildable {
				return &cliError{kind: kindError, err: fmt.Errorf("job %s is disabled", job.FullName)}
			}
			if err := validateParams(job, values); err != nil {
				return err
			}
			qid, err := c.Trigger(ctx, job.FullName, values, len(job.Params) > 0)
			if err != nil {
				return err
			}
			a.progressf("Queued %s (queue item %d)", job.FullName, qid)
			wo.server, wo.job = a.server, job.FullName
			return a.fromQueue(ctx, c, qid, wo)
		},
	}
	a.serverFlag(cmd)
	cmd.Flags().StringArrayVarP(&params, "param", "p", nil, "parameter KEY=VALUE (repeatable)")
	cmd.Flags().BoolVar(&wo.watch, "watch", false, "wait for the run to finish; exit code reflects its result")
	wo.register(cmd)
	return cmd
}

func (a *app) runsWatchCmd() *cobra.Command {
	var (
		wo      watchOpts
		queueID int
	)
	cmd := &cobra.Command{
		Use:   "watch -s SERVER JOB (BUILD | --queue ID)",
		Short: "Wait for a run to finish and print its result",
		Long: `Wait for run BUILD of JOB (or the run started from queue item ID) to finish
and print it. A result other than SUCCESS is reported as error kind
"run_failed" with the last 50 log lines. A run that already finished returns
immediately, so this is also how to check a past run. For more of the log, use
'jenkinsctl runs logs'.

If --timeout expires first, the error has kind "timeout" and a "resume"
command that continues waiting. For long runs, use --timeout 0 to wait as
long as it takes.

` + jobHelp,
		Example: `  jenkinsctl runs watch -s prod team/api/main 142
  jenkinsctl runs watch -s prod team/api/main 142 --timeout 0
  jenkinsctl runs watch -s prod team/api/main --queue 9812`,
		Args: func(_ *cobra.Command, args []string) error {
			if queueID > 0 {
				return exactArgs("JOB")(nil, args)
			}
			return exactArgs("JOB", "BUILD")(nil, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			wo.watch = true
			if err := wo.check(); err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			job, err := c.JobName(args[0])
			if err != nil {
				return err
			}
			wo.server, wo.job = a.server, job
			if queueID > 0 {
				return a.fromQueue(cmd.Context(), c, queueID, wo)
			}
			n, err := parseBuild(args[1])
			if err != nil {
				return err
			}
			ctx, cancel := withOptionalTimeout(cmd.Context(), wo.timeout)
			defer cancel()
			return a.watchBuild(ctx, cmd.Context(), c, n, wo)
		},
	}
	a.serverFlag(cmd)
	cmd.Flags().IntVar(&queueID, "queue", 0, "queue item id from 'runs submit' instead of BUILD")
	wo.register(cmd)
	return cmd
}

// defaultTimeout keeps a wait under Claude Code's 2-minute Bash limit; a
// timed-out wait reports a resume command instead of being killed silently.
const defaultTimeout = 100 * time.Second

// failureTail is how many log lines a run that did not succeed includes.
const failureTail = 50

type watchOpts struct {
	watch    bool
	timeout  time.Duration
	interval time.Duration

	server, job string
}

func (o *watchOpts) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.DurationVar(&o.timeout, "timeout", defaultTimeout, "stop waiting after this long and report a resume command (0 = no limit)")
	registerInterval(f, &o.interval)
}

func registerInterval(f *pflag.FlagSet, d *time.Duration) {
	f.DurationVar(d, "interval", 3*time.Second, "poll interval")
	_ = f.MarkHidden("interval")
}

func (o *watchOpts) check() error {
	if o.timeout < 0 {
		return usageErr("--timeout must not be negative")
	}
	return nil
}

func parseBuild(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, usageErr("BUILD must be a positive build number, got %q", s)
	}
	return n, nil
}

// resume rebuilds the watch command that continues an interrupted wait.
func (o *watchOpts) resume(number, queueID int) string {
	parts := []string{"jenkinsctl", "runs", "watch", "-s", shellQuote(o.server), shellQuote(o.job)}
	if queueID > 0 {
		parts = append(parts, "--queue", strconv.Itoa(queueID))
	} else {
		parts = append(parts, strconv.Itoa(number))
	}
	parts = append(parts, "--timeout", o.timeout.String())
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	safe := s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./%:=@+", r))
	}) < 0
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type queued struct {
	Job      string `json:"job"`
	Status   string `json:"status"`
	QueueID  int    `json:"queue_id"`
	QueueURL string `json:"queue_url"`
	Why      string `json:"why,omitempty"`
}

// fromQueue waits for a queue item to become a run, then prints it or keeps
// watching. --timeout covers both phases.
func (a *app) fromQueue(parent context.Context, c *jenkins.Client, qid int, o watchOpts) error {
	ctx, cancel := withOptionalTimeout(parent, o.timeout)
	defer cancel()

	lastWhy := ""
	q, err := c.WaitForQueue(ctx, qid, o.interval, func(q *jenkins.QueueItem) {
		if q.Why != "" && q.Why != lastWhy {
			a.progressf("Waiting in queue: %s", q.Why)
			lastWhy = q.Why
		}
	})
	switch {
	case errors.Is(err, jenkins.ErrQueueCancelled):
		return &cliError{kind: kindQueueCancelled, err: err}
	case err != nil && ctx.Err() != nil && parent.Err() == nil:
		out := queued{Job: o.job, Status: "QUEUED", QueueID: qid, QueueURL: fmt.Sprintf("%s/queue/item/%d/", c.BaseURL(), qid), Why: lastWhy}
		return &cliError{
			kind:   kindTimeout,
			err:    fmt.Errorf("run is still queued after %s: %s", o.timeout, orDefault(lastWhy, "no reason given")),
			resume: o.resume(0, qid),
			data:   out,
			text:   func(w io.Writer) { fmt.Fprintf(w, "%s is still queued (queue item %d)\n", o.job, qid) },
		}
	case jenkins.IsStatus(err, 404):
		return &cliError{kind: kindNotFound, err: fmt.Errorf(
			"queue item %d not found; Jenkins forgets queue items minutes after they start, find the run with 'jenkinsctl runs list -s %s %s'", qid, o.server, shellQuote(o.job))}
	case err != nil:
		return err
	}
	a.progressf("Started %s #%d", o.job, q.BuildNum)
	if !o.watch {
		b, err := c.GetBuild(parent, o.job, q.BuildNum)
		if err != nil {
			return err
		}
		return a.emit(b, func(w io.Writer) { buildDetail(w, b) })
	}
	return a.watchBuild(ctx, parent, c, q.BuildNum, o)
}

// watchBuild polls under ctx (which may carry --timeout) and reports under
// parent so the final output still works after a timeout.
func (a *app) watchBuild(ctx, parent context.Context, c *jenkins.Client, number int, o watchOpts) error {
	job := o.job
	var (
		b       *jenkins.Build
		lastSt  string
		pollErr error
	)
	for {
		nb, err := c.GetBuild(ctx, job, number)
		if err != nil {
			pollErr = err
			break
		}
		b = nb
		if b.Status != lastSt {
			a.progressf("%s #%d: %s", job, number, b.Status)
			lastSt = b.Status
		}
		if b.Done() {
			break
		}
		if err := jenkins.Sleep(ctx, o.interval); err != nil {
			pollErr = err
			break
		}
	}

	timedOut := pollErr != nil && ctx.Err() != nil && parent.Err() == nil
	if pollErr != nil && !timedOut {
		return a.explainNotFound(parent, c, job, number, pollErr)
	}
	if b == nil {
		var err error
		if b, err = c.GetBuild(parent, job, number); err != nil {
			return err
		}
	}
	if b.Done() && b.Result != "SUCCESS" {
		lines, err := c.LogTail(parent, job, number, failureTail)
		if err != nil {
			return err
		}
		b.LogTail = lines
	}
	text := func(w io.Writer) { buildDetail(w, b) }
	switch {
	case timedOut:
		return &cliError{kind: kindTimeout, err: fmt.Errorf("%s #%d still %s after %s", job, number, b.Status, o.timeout),
			resume: o.resume(number, 0), data: b, text: text}
	case b.Result != "SUCCESS":
		return &cliError{kind: kindRunFailed, err: fmt.Errorf("%s #%d finished with %s", job, number, b.Result), data: b, text: text}
	}
	return a.emit(b, text)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func withOptionalTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

func parseParams(kv []string) (url.Values, error) {
	v := url.Values{}
	for _, p := range kv {
		k, val, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, usageErr("invalid --param %q: want KEY=VALUE", p)
		}
		v.Add(k, val)
	}
	return v, nil
}

func validateParams(job *jenkins.Job, values url.Values) error {
	defs := map[string]jenkins.ParamDef{}
	names := make([]string, 0, len(job.Params))
	for _, d := range job.Params {
		defs[d.Name] = d
		names = append(names, d.Name)
	}
	var unknown []string
	for k, vals := range values {
		d, ok := defs[k]
		if !ok {
			unknown = append(unknown, k)
			continue
		}
		for _, val := range vals {
			if len(d.Choices) > 0 && !contains(d.Choices, val) {
				return &cliError{kind: kindInvalidParam, err: fmt.Errorf("parameter %s: %q is not an allowed choice", k, val), valid: d.Choices}
			}
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	if len(names) == 0 {
		return &cliError{kind: kindInvalidParam, err: fmt.Errorf("job %s takes no parameters, got %v", job.FullName, unknown)}
	}
	return &cliError{kind: kindInvalidParam, err: fmt.Errorf("unknown parameter(s) %v for %s", unknown, job.FullName), valid: names}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func buildsTable(w io.Writer, builds []*jenkins.Build) {
	if len(builds) == 0 {
		fmt.Fprintln(w, "No runs.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tSTATUS\tSTARTED\tDURATION\tPARAMS")
	for _, b := range builds {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", b.Number, b.Status, ago(b.StartedAt), fmtMs(b.DurationMs), fmtParams(b.Parameters))
	}
	tw.Flush()
}

func buildDetail(w io.Writer, b *jenkins.Build) {
	fmt.Fprintf(w, "%s #%d  %s\n", b.Job, b.Number, b.Status)
	fmt.Fprintf(w, "  url:      %s\n", b.URL)
	fmt.Fprintf(w, "  started:  %s (%s)\n", b.StartedAt.Local().Format(time.DateTime), ago(b.StartedAt))
	dur := fmtMs(b.DurationMs)
	if b.Building && b.EstimatedDurationMs > 0 {
		dur += " of ~" + fmtMs(b.EstimatedDurationMs)
	}
	fmt.Fprintf(w, "  duration: %s\n", dur)
	if b.Description != "" {
		fmt.Fprintf(w, "  desc:     %s\n", b.Description)
	}
	for _, c := range b.Causes {
		fmt.Fprintf(w, "  cause:    %s\n", c.Description)
	}
	if len(b.Parameters) > 0 {
		fmt.Fprintf(w, "  params:   %s\n", fmtParams(b.Parameters))
	}
	if len(b.LogTail) > 0 {
		fmt.Fprintln(w, "  --- log tail ---")
		for _, l := range b.LogTail {
			fmt.Fprintln(w, "  "+l)
		}
	}
}

func fmtParams(p map[string]any) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, p[k]))
	}
	return strings.Join(parts, " ")
}

func fmtMs(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
