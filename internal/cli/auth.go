package cli

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/priyanshu-shubham/jenkinsctl/internal/config"
)

func (a *app) authCmd() *cobra.Command {
	return group(&cobra.Command{
		Use:   "auth",
		Short: "Add, list and remove Jenkins servers",
	}, a.authLoginCmd(), a.authListCmd(), a.authLogoutCmd())
}

func (a *app) authLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Interactively add a server and verify its credentials",
		Long: `Prompts for a server name, URL, username and API token, verifies them
against Jenkins, and saves the profile to ~/.config/jenkinsctl/config.json (mode 0600,
override the path with JENKINSCTL_CONFIG). Nothing is saved if verification fails.

Answers may also be piped on stdin, one per line, in prompt order.`,
		Args: exactArgs(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			p := newPrompter(a.stdin, a.stderr)

			name, err := p.ask("Server name (passed as -s NAME)", func(v string) error {
				if strings.ContainsAny(v, " \t/") {
					return errors.New("use letters, digits, '-' or '_'")
				}
				return nil
			})
			if err != nil {
				return err
			}
			if _, exists := cfg.Servers[name]; exists {
				ok, err := p.confirm(fmt.Sprintf("Server %q already exists. Replace it?", name))
				if err != nil {
					return err
				}
				if !ok {
					return usageErr("login cancelled; %q left unchanged", name)
				}
			}
			s := &config.Server{}
			if s.URL, err = p.ask("Jenkins URL", validateURL); err != nil {
				return err
			}
			s.URL = strings.TrimRight(s.URL, "/")
			if s.User, err = p.ask("Username", nil); err != nil {
				return err
			}
			fmt.Fprintf(a.stderr, "Create a token under your user menu → Security → API Token (%s/me/security/)\n", s.URL)
			if s.Token, err = p.secret("API token"); err != nil {
				return err
			}

			who, err := a.verifyLogin(cmd.Context(), p, s)
			if err != nil {
				return err
			}
			cfg.Servers[name] = s
			if err := cfg.Save(); err != nil {
				return err
			}
			out := map[string]any{"name": name, "url": s.URL, "user": who}
			return a.emit(out, func(w io.Writer) {
				fmt.Fprintf(w, "✓ Logged in to %s as %s. Use it with: jenkinsctl runs list -s %s JOB\n", s.URL, who, name)
			})
		},
	}
}

// verifyLogin checks credentials, offering to skip TLS verification once if
// the server's certificate is not trusted.
func (a *app) verifyLogin(ctx context.Context, p *prompter, s *config.Server) (string, error) {
	for {
		c, err := newClient(s)
		if err != nil {
			return "", usageErr("%v", err)
		}
		me, err := c.WhoAmI(ctx)
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) && !s.Insecure {
			ok, perr := p.confirm(fmt.Sprintf("TLS certificate for %s is not trusted (%v). Skip verification for this server?", s.URL, certErr.Err))
			if perr != nil {
				return "", perr
			}
			if ok {
				s.Insecure = true
				continue
			}
		}
		if err != nil {
			return "", err
		}
		// Jenkins may answer anonymously instead of returning 401.
		if me.ID == "" || me.ID == "anonymous" {
			return "", &cliError{kind: kindAuth, err: errors.New("Jenkins treated the request as anonymous; check username and token")}
		}
		return me.ID, nil
	}
}

func validateURL(v string) error {
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("must look like https://jenkins.example.com")
	}
	return nil
}

type serverEntry struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	User     string `json:"user"`
	Insecure bool   `json:"insecure,omitempty"`
}

func (a *app) authListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured servers (no network access)",
		Args:  exactArgs(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			out := []serverEntry{}
			for _, n := range cfg.Names() {
				s := cfg.Servers[n]
				out = append(out, serverEntry{Name: n, URL: s.URL, User: s.User, Insecure: s.Insecure})
			}
			return a.emit(out, func(w io.Writer) {
				if len(out) == 0 {
					fmt.Fprintln(w, "No servers configured. Run 'jenkinsctl auth login'.")
					return
				}
				tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tURL\tUSER")
				for _, e := range out {
					fmt.Fprintf(tw, "%s\t%s\t%s\n", e.Name, e.URL, e.User)
				}
				tw.Flush()
			})
		},
	}
}

func (a *app) authLogoutCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logout -s NAME",
		Short: "Remove a server and its stored token",
		Args:  exactArgs(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, _, err := a.selected()
			if err != nil {
				return err
			}
			delete(cfg.Servers, a.server)
			if err := cfg.Save(); err != nil {
				return err
			}
			return a.emit(map[string]any{"removed": a.server}, func(w io.Writer) {
				fmt.Fprintf(w, "Removed server %q\n", a.server)
			})
		},
	}
	a.serverFlag(cmd)
	return cmd
}

// prompter reads answers line by line from one buffered reader so piped
// input is not lost between prompts.
type prompter struct {
	in  *bufio.Reader
	raw io.Reader
	out io.Writer
}

func newPrompter(in io.Reader, out io.Writer) *prompter {
	return &prompter{in: bufio.NewReader(in), raw: in, out: out}
}

func (p *prompter) line() (string, error) {
	s, err := p.in.ReadString('\n')
	if err == io.EOF && s != "" {
		err = nil
	}
	if err == io.EOF {
		return "", usageErr("login is interactive: a human must run 'jenkinsctl auth login' in a terminal (or pipe one answer per line)")
	}
	return strings.TrimSpace(s), err
}

func (p *prompter) ask(label string, validate func(string) error) (string, error) {
	for {
		fmt.Fprintf(p.out, "%s: ", label)
		v, err := p.line()
		if err != nil {
			return "", err
		}
		if v == "" {
			fmt.Fprintln(p.out, "  required")
		} else if validate != nil {
			if err := validate(v); err != nil {
				fmt.Fprintf(p.out, "  %v\n", err)
			} else {
				return v, nil
			}
		} else {
			return v, nil
		}
		if !isTerminal(p.raw) {
			return "", usageErr("invalid answer for %q", label)
		}
	}
}

func (p *prompter) secret(label string) (string, error) {
	if f, ok := p.raw.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(p.out, "%s (hidden): ", label)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(p.out)
		if err != nil {
			return "", err
		}
		if v := strings.TrimSpace(string(b)); v != "" {
			return v, nil
		}
		return "", usageErr("%s is required", label)
	}
	return p.ask(label, nil)
}

func (p *prompter) confirm(question string) (bool, error) {
	fmt.Fprintf(p.out, "%s [y/N]: ", question)
	v, err := p.line()
	if err != nil {
		return false, err
	}
	v = strings.ToLower(v)
	return v == "y" || v == "yes", nil
}
