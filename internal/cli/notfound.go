package cli

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/priyanshu-shubham/jenkinsctl/internal/jenkins"
)

// maxSuggestions caps error.valid so a large root folder doesn't flood the output.
const maxSuggestions = 50

// explainNotFound turns a bare 404 into an error that says what is missing:
// the run (with the latest run number) or the job (with the jobs that do exist
// in the nearest existing folder). number is 0 when no run was involved.
func (a *app) explainNotFound(ctx context.Context, c *jenkins.Client, job string, number int, err error) error {
	if !jenkins.IsStatus(err, http.StatusNotFound) {
		return err
	}
	name, nerr := c.JobName(job)
	if nerr != nil {
		return err
	}
	if number > 0 {
		j, jerr := c.GetJob(ctx, name)
		if jerr == nil {
			latest := "it has no runs yet"
			if j.LastBuild > 0 {
				latest = fmt.Sprintf("the latest is #%d", j.LastBuild)
			}
			return &cliError{kind: kindNotFound, err: fmt.Errorf("job %s has no run #%d; %s", name, number, latest)}
		}
		if !jenkins.IsStatus(jerr, http.StatusNotFound) {
			return err
		}
	}

	ce := &cliError{kind: kindNotFound, err: fmt.Errorf("job %s not found on server %s", name, a.server)}
	for folder := parentOf(name); ; folder = parentOf(folder) {
		children, cerr := c.ChildJobs(ctx, folder)
		if cerr == nil {
			where := "the top level"
			if folder != "" {
				where = "folder " + folder
			}
			msg := fmt.Sprintf("job %s not found on server %s; error.valid lists the jobs in %s", name, a.server, where)
			if len(children) > maxSuggestions {
				msg += fmt.Sprintf(" (first %d of %d)", maxSuggestions, len(children))
				children = children[:maxSuggestions]
			}
			ce.err, ce.valid = fmt.Errorf("%s", msg), children
			break
		}
		if folder == "" || !jenkins.IsStatus(cerr, http.StatusNotFound) {
			break
		}
	}
	return ce
}

func parentOf(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[:i]
	}
	return ""
}
