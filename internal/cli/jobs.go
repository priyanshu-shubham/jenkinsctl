package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func (a *app) jobsCmd() *cobra.Command {
	return group(&cobra.Command{
		Use:   "jobs",
		Short: "Inspect jobs",
	}, a.jobsShowCmd())
}

func (a *app) jobsShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "show -s SERVER JOB",
		Short:   "Show a job and the parameters 'runs submit' accepts",
		Long:    "Show a job and the parameters 'runs submit' accepts.\n\n" + jobHelp,
		Example: "  jenkinsctl jobs show -s prod team/api/main",
		Args:    exactArgs("JOB"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			j, err := c.GetJob(cmd.Context(), args[0])
			if err != nil {
				return a.explainNotFound(cmd.Context(), c, args[0], 0, err)
			}
			return a.emit(j, func(w io.Writer) {
				fmt.Fprintf(w, "%s\n  url:        %s\n  buildable:  %v\n", j.FullName, j.URL, j.Buildable)
				if j.LastBuild > 0 {
					fmt.Fprintf(w, "  last run:   #%d\n", j.LastBuild)
				}
				if len(j.Params) == 0 {
					fmt.Fprintln(w, "  parameters: none")
					return
				}
				fmt.Fprintln(w, "  parameters:")
				tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
				for _, p := range j.Params {
					extra := ""
					if len(p.Choices) > 0 {
						extra = "one of " + strings.Join(p.Choices, "|")
					} else if p.Default != nil && p.Default != "" {
						extra = fmt.Sprintf("default %v", p.Default)
					}
					fmt.Fprintf(tw, "    %s\t%s\t%s\t%s\n", p.Name, p.Type, extra, p.Description)
				}
				tw.Flush()
			})
		},
	}
	a.serverFlag(cmd)
	return cmd
}
