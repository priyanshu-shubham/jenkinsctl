package cli

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

func (a *app) apiCmd() *cobra.Command {
	var method, data string
	cmd := &cobra.Command{
		Use:   "api -s SERVER PATH",
		Short: "Send a raw authenticated request to Jenkins",
		Long: `Send a request to PATH on the server, using the saved credentials, and print
the response body unchanged. Use it for anything the other commands don't
cover, such as aborting a run or cancelling a queued one.

PATH is relative to the server's base URL (e.g. /queue/api/json) or a full URL
on the same server. A CSRF crumb is added automatically for non-GET requests.

Redirects are followed only for GET. When the response has no body, as with
most actions, the output is {"http_status":N} plus "location" if Jenkins
redirected. HTTP errors are reported like any other error, with http_status.

This can do anything your Jenkins user is allowed to do, including deleting
jobs.`,
		Example: `  jenkinsctl api -s prod '/queue/api/json?tree=items[id,why,task[name]]'
  jenkinsctl api -s prod -X POST /job/team/job/api/job/main/142/stop
  jenkinsctl api -s prod -X POST /queue/cancelItem -d id=981`,
		Args: exactArgs("PATH"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var body io.Reader
			ct := ""
			if data != "" {
				body = strings.NewReader(data)
				if data == "@-" {
					body = a.stdin
				}
				ct = "application/x-www-form-urlencoded"
			}
			m := strings.ToUpper(method)
			if m == "" {
				m = http.MethodGet
				if body != nil {
					m = http.MethodPost
				}
			}
			resp, err := c.Do(cmd.Context(), m, args[0], ct, body)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			b, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			if len(b) > 0 {
				_, err = a.stdout.Write(b)
				return err
			}
			out := map[string]any{"http_status": resp.StatusCode}
			if loc := resp.Header.Get("Location"); loc != "" {
				out["location"] = loc
			}
			return a.emit(out, func(w io.Writer) {
				fmt.Fprintf(w, "HTTP %d %s\n", resp.StatusCode, resp.Header.Get("Location"))
			})
		},
	}
	a.serverFlag(cmd)
	cmd.Flags().StringVarP(&method, "method", "X", "", "HTTP method (default GET, or POST when --data is given)")
	cmd.Flags().StringVarP(&data, "data", "d", "", "form-encoded request body, e.g. id=981; @- reads it from stdin")
	return cmd
}
