package cli

import (
	"bytes"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/priyanshu-shubham/jenkinsctl/internal/jenkins"
)

func (a *app) runsLogsCmd() *cobra.Command {
	var (
		tail     int
		follow   bool
		interval time.Duration
	)
	cmd := &cobra.Command{
		Use:   "logs -s SERVER JOB BUILD",
		Short: "Print a run's console log as plain text",
		Long: `Print the console log of run BUILD as plain text (-o has no effect).

Without --follow, prints the log as it is right now and exits, even if the run
is still going. With --follow, keeps printing new output until the run
finishes; there is no time limit, and Ctrl-C stops it cleanly.

--tail N prints only the last N lines. With --follow it then continues from
there, like tail -f.

A note on stderr says when the run is still going or how it finished. The exit
code is 0 whenever the log was fetched, whatever the run's result.

` + jobHelp,
		Example: `  jenkinsctl runs logs -s prod team/api/main 142 --tail 200
  jenkinsctl runs logs -s prod team/api/main 142 > run-142.log
  jenkinsctl runs logs -s prod team/api/main 142 --tail 50 --follow`,
		Args: exactArgs("JOB", "BUILD"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if tail < 0 {
				return usageErr("--tail must not be negative")
			}
			n, err := parseBuild(args[1])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			c, err := a.client()
			if err != nil {
				return err
			}
			job, err := c.JobName(args[0])
			if err != nil {
				return err
			}
			// Also confirms the run exists, so a 404 can be explained precisely.
			b, err := c.GetBuild(ctx, job, n)
			if err != nil {
				return a.explainNotFound(ctx, c, job, n, err)
			}

			var out io.Writer = a.stdout
			tw := &tailWriter{n: tail}
			if tail > 0 {
				out = tw
			}
			if !follow {
				if err := c.ConsoleText(ctx, job, n, out); err != nil {
					return err
				}
				if tail > 0 {
					if err := tw.flush(a.stdout); err != nil {
						return err
					}
				}
				if b.Building {
					fmt.Fprintf(a.stderr, "[jenkinsctl] %s #%d is still running; this is the log so far (add --follow to keep reading)\n", job, n)
				} else {
					fmt.Fprintf(a.stderr, "[jenkinsctl] %s #%d finished with %s\n", job, n, b.Status)
				}
				return nil
			}

			// Progressive offsets count raw log bytes, so following can't start
			// from a consoleText read; it reads from 0 and drains to the end.
			var off int64
			caughtUp := false
			for {
				next, more, err := c.ProgressiveLog(ctx, job, n, off, out)
				if err != nil {
					return err
				}
				progressed := next != off
				off = next
				if !caughtUp && (!more || !progressed) {
					caughtUp = true
					if tail > 0 {
						if err := tw.flush(a.stdout); err != nil {
							return err
						}
						out = a.stdout
					}
				}
				if !more {
					break
				}
				if !progressed {
					if err := jenkins.Sleep(ctx, interval); err != nil {
						return err
					}
				}
			}
			if b, err := c.GetBuild(ctx, job, n); err == nil && b.Done() {
				fmt.Fprintf(a.stderr, "[jenkinsctl] %s #%d finished with %s\n", job, n, b.Status)
			}
			return nil
		},
	}
	a.serverFlag(cmd)
	cmd.Flags().IntVarP(&tail, "tail", "n", 0, "print only the last N lines")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new output until the run finishes")
	registerInterval(cmd.Flags(), &interval)
	return cmd
}

// tailWriter keeps only the last n complete lines plus any unterminated
// remainder, so --tail never holds a whole multi-megabyte log in memory.
type tailWriter struct {
	n       int
	lines   [][]byte
	partial []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.partial = append(t.partial, p...)
	for {
		i := bytes.IndexByte(t.partial, '\n')
		if i < 0 {
			break
		}
		t.lines = append(t.lines, append([]byte(nil), t.partial[:i+1]...))
		if len(t.lines) > t.n {
			t.lines = t.lines[1:]
		}
		t.partial = t.partial[i+1:]
	}
	return len(p), nil
}

func (t *tailWriter) flush(w io.Writer) error {
	for _, l := range t.lines {
		if _, err := w.Write(l); err != nil {
			return err
		}
	}
	_, err := w.Write(t.partial)
	return err
}
