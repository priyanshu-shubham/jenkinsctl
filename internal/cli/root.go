// Package cli wires the jenkinsctl command tree.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/priyanshu-shubham/jenkinsctl/internal/config"
	"github.com/priyanshu-shubham/jenkinsctl/internal/jenkins"
)

type app struct {
	server  string
	output  string
	jsonOut bool

	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func Execute(version string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, version)
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, version ...string) int {
	a := &app{stdin: stdin, stdout: stdout, stderr: stderr}
	root := a.rootCmd()
	if len(version) > 0 {
		root.Version = version[0]
	}
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		// Interrupted (Ctrl-C): the user asked to stop, so no error report.
		return ExitInterrupted
	}
	// Flag parse errors happen before PersistentPreRunE resolves the format.
	_ = a.resolveOutput()
	a.report(classify(err))
	return ExitError
}

func (a *app) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "jenkinsctl",
		Short: "Jenkins CLI for humans and agents",
		Long: `jenkinsctl talks to one or more Jenkins servers.

Add servers with 'jenkinsctl auth login'. Every command that talks to Jenkins takes
-s/--server NAME; there is no implicit default server.

Output is JSON when stdout is not a terminal (compact, one document per
command); force a format with -o json|text.

On failure the exit code is 1 and the JSON carries an "error" object:
  {"error":{"kind":"...","message":"...","resume":"...","valid":[...]}}
alongside any partial result (e.g. the run's state when a wait timed out).
If "resume" is present, running it continues where the command stopped.

Error kinds: usage, invalid_parameter, auth, not_found, timeout, run_failed,
queue_cancelled, server_error, network, error.`,
		SilenceErrors:     true,
		SilenceUsage:      true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return a.resolveOutput()
		},
	}
	root.PersistentFlags().StringVarP(&a.output, "output", "o", "auto", "output format: auto|json|text")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &cliError{kind: kindUsage, err: err}
	})
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	root.AddCommand(a.authCmd(), a.runsCmd(), a.jobsCmd())
	return root
}

func (a *app) resolveOutput() error {
	switch a.output {
	case "json":
		a.jsonOut = true
	case "text":
		a.jsonOut = false
	case "auto", "":
		a.jsonOut = !isTerminal(a.stdout)
	default:
		return usageErr("invalid --output %q: want auto, json or text", a.output)
	}
	return nil
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func (a *app) loadConfig() (*config.Config, error) {
	p, err := config.DefaultPath()
	if err != nil {
		return nil, err
	}
	return config.Load(p)
}

// serverFlag adds the required -s/--server flag to commands that hit Jenkins.
func (a *app) serverFlag(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&a.server, "server", "s", "", "server name from 'jenkinsctl auth list' (required)")
	_ = cmd.RegisterFlagCompletionFunc("server", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		cfg, err := a.loadConfig()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		return cfg.Names(), cobra.ShellCompDirectiveNoFileComp
	})
}

// selected returns the profile named by --server, with a helpful error listing
// the alternatives when it is missing or unknown.
func (a *app) selected() (*config.Config, *config.Server, error) {
	cfg, err := a.loadConfig()
	if err != nil {
		return nil, nil, err
	}
	if len(cfg.Servers) == 0 {
		return nil, nil, usageErr("no servers configured; a human must run 'jenkinsctl auth login' first")
	}
	if a.server == "" {
		return nil, nil, usageErr("--server is required (configured: %v)", cfg.Names())
	}
	s, ok := cfg.Servers[a.server]
	if !ok {
		return nil, nil, usageErr("unknown server %q (configured: %v)", a.server, cfg.Names())
	}
	return cfg, s, nil
}

func (a *app) client() (*jenkins.Client, error) {
	_, s, err := a.selected()
	if err != nil {
		return nil, err
	}
	return newClient(s)
}

func newClient(s *config.Server) (*jenkins.Client, error) {
	return jenkins.New(jenkins.Options{URL: s.URL, User: s.User, Token: s.Token, Insecure: s.Insecure})
}

// emit writes v as JSON, or calls text for human output.
func (a *app) emit(v any, text func(w io.Writer)) error {
	if !a.jsonOut {
		text(a.stdout)
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return a.writeJSON(b)
}

// writeJSON keeps output compact for agents and indents only for a person at a terminal.
func (a *app) writeJSON(b []byte) error {
	if isTerminal(a.stdout) {
		var buf bytes.Buffer
		if err := json.Indent(&buf, b, "", "  "); err == nil {
			b = buf.Bytes()
		}
	}
	_, err := a.stdout.Write(append(b, '\n'))
	return err
}

// progressf is for humans only; JSON mode keeps stderr quiet.
func (a *app) progressf(format string, args ...any) {
	if !a.jsonOut {
		fmt.Fprintf(a.stderr, format+"\n", args...)
	}
}

// report prints a failure. In JSON mode it is a single stdout document: the
// partial result (if any) with an "error" field added.
func (a *app) report(ce *cliError) {
	info := ce.info()
	if !a.jsonOut {
		if ce.data != nil && ce.text != nil {
			ce.text(a.stdout)
		}
		fmt.Fprintf(a.stderr, "error: %s\n", info.Message)
		if len(info.Valid) > 0 {
			fmt.Fprintf(a.stderr, "valid: %s\n", strings.Join(info.Valid, ", "))
		}
		if info.Resume != "" {
			fmt.Fprintf(a.stderr, "resume: %s\n", info.Resume)
		}
		return
	}
	eb, _ := json.Marshal(info)
	doc := []byte(`{"error":` + string(eb) + `}`)
	if ce.data != nil {
		// Splice "error" into the object so field order stays as the struct defines it.
		if db, err := json.Marshal(ce.data); err == nil && len(db) > 2 && db[0] == '{' {
			doc = append(append(db[:len(db)-1], `,"error":`...), append(eb, '}')...)
		}
	}
	_ = a.writeJSON(doc)
}

func exactArgs(names ...string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != len(names) {
			return usageErr("expected %d argument(s) %v, got %d", len(names), names, len(args))
		}
		return nil
	}
}

// group makes a parent command reject unknown subcommands with a usage error
// instead of printing help and exiting 0.
func group(cmd *cobra.Command, subs ...*cobra.Command) *cobra.Command {
	cmd.Args = cobra.ArbitraryArgs
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if len(args) > 0 {
			return usageErr("unknown command %q for %q", args[0], c.CommandPath())
		}
		return c.Help()
	}
	cmd.AddCommand(subs...)
	return cmd
}
