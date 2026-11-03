package jenkins

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// JobPath converts "folder/sub/job" (or a job URL) into "/job/folder/job/sub/job/job".
func (c *Client) JobPath(job string) (string, error) {
	name, err := c.JobName(job)
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", nil
	}
	var b strings.Builder
	for _, seg := range strings.Split(name, "/") {
		b.WriteString("/job/")
		b.WriteString(url.PathEscape(seg))
	}
	return b.String(), nil
}

// JobName normalizes a job URL or slash path into Jenkins' full name.
func (c *Client) JobName(job string) (string, error) {
	job = strings.TrimSpace(job)
	if strings.HasPrefix(job, "http://") || strings.HasPrefix(job, "https://") {
		u, err := c.resolve(job)
		if err != nil {
			return "", err
		}
		rel := strings.TrimPrefix(u.Path, strings.TrimRight(c.base.Path, "/"))
		segs := strings.Split(strings.Trim(rel, "/"), "/")
		var names []string
		for i := 0; i+1 < len(segs); i++ {
			if segs[i] == "job" {
				names = append(names, segs[i+1])
				i++
			}
		}
		if len(names) == 0 {
			return "", fmt.Errorf("%w: no job found in URL %s", ErrInvalidInput, job)
		}
		return strings.Join(names, "/"), nil
	}
	// Accept URL-path fragments like "folder/job/sub/job/main": "job" followed
	// by another segment is Jenkins' separator, not a job name.
	segs := strings.Split(strings.Trim(job, "/"), "/")
	names := make([]string, 0, len(segs))
	for i, seg := range segs {
		if seg == "" || (seg == "job" && i+1 < len(segs)) {
			continue
		}
		names = append(names, seg)
	}
	return strings.Join(names, "/"), nil
}

// ChildJobs returns the full names of the jobs directly inside folder ("" for the root).
func (c *Client) ChildJobs(ctx context.Context, folder string) ([]string, error) {
	p, err := c.JobPath(folder)
	if err != nil {
		return nil, err
	}
	var r struct {
		Jobs []struct {
			FullName string `json:"fullName"`
		} `json:"jobs"`
	}
	if err := c.GetJSON(ctx, p+"/api/json?tree="+url.QueryEscape("jobs[fullName]"), &r); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(r.Jobs))
	for _, j := range r.Jobs {
		names = append(names, j.FullName)
	}
	sort.Strings(names)
	return names, nil
}

// ErrInvalidInput marks caller mistakes (as opposed to server failures).
var ErrInvalidInput = errors.New("invalid input")

type Me struct {
	ID       string `json:"id"`
	FullName string `json:"fullName"`
}

func (c *Client) WhoAmI(ctx context.Context) (*Me, error) {
	var me Me
	if err := c.GetJSON(ctx, "/me/api/json?tree=id,fullName", &me); err != nil {
		return nil, err
	}
	return &me, nil
}

type ParamDef struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Default     any      `json:"default,omitempty"`
	Choices     []string `json:"choices,omitempty"`
}

type Job struct {
	FullName  string     `json:"full_name"`
	URL       string     `json:"url"`
	Buildable bool       `json:"buildable"`
	LastBuild int        `json:"last_build,omitempty"`
	Params    []ParamDef `json:"parameters"`
	Class     string     `json:"-"`
}

// IsFolder is true for containers (Folder, OrganizationFolder, multibranch).
func (j *Job) IsFolder() bool {
	return strings.Contains(j.Class, "Folder") || strings.Contains(j.Class, "MultiBranch")
}

type rawJob struct {
	FullName  string `json:"fullName"`
	URL       string `json:"url"`
	Class     string `json:"_class"`
	Buildable bool   `json:"buildable"`
	LastBuild *struct {
		Number int `json:"number"`
	} `json:"lastBuild"`
	Property []struct {
		ParameterDefinitions []struct {
			Name                  string   `json:"name"`
			Type                  string   `json:"type"`
			Description           string   `json:"description"`
			Choices               []string `json:"choices"`
			DefaultParameterValue *struct {
				Value any `json:"value"`
			} `json:"defaultParameterValue"`
		} `json:"parameterDefinitions"`
	} `json:"property"`
}

func (r *rawJob) toJob() *Job {
	j := &Job{FullName: r.FullName, URL: r.URL, Class: r.Class, Buildable: r.Buildable, Params: []ParamDef{}}
	if r.LastBuild != nil {
		j.LastBuild = r.LastBuild.Number
	}
	for _, p := range r.Property {
		for _, d := range p.ParameterDefinitions {
			pd := ParamDef{Name: d.Name, Type: d.Type, Description: d.Description, Choices: d.Choices}
			if d.DefaultParameterValue != nil {
				pd.Default = d.DefaultParameterValue.Value
			}
			j.Params = append(j.Params, pd)
		}
	}
	return j
}

const jobTree = "fullName,url,buildable,lastBuild[number]," +
	"property[parameterDefinitions[name,type,description,choices,defaultParameterValue[value]]]"

func (c *Client) GetJob(ctx context.Context, job string) (*Job, error) {
	p, err := c.JobPath(job)
	if err != nil {
		return nil, err
	}
	if p == "" {
		return nil, fmt.Errorf("%w: job name is required", ErrInvalidInput)
	}
	var r rawJob
	if err := c.GetJSON(ctx, p+"/api/json?tree="+url.QueryEscape(jobTree), &r); err != nil {
		return nil, err
	}
	return r.toJob(), nil
}

type Cause struct {
	Description string `json:"description"`
	UserID      string `json:"user_id,omitempty"`
}

type Build struct {
	Job                 string         `json:"job"`
	Number              int            `json:"number"`
	URL                 string         `json:"url"`
	Status              string         `json:"status"`
	Result              string         `json:"result,omitempty"`
	Building            bool           `json:"building"`
	StartedAt           time.Time      `json:"started_at"`
	DurationMs          int64          `json:"duration_ms"`
	EstimatedDurationMs int64          `json:"estimated_duration_ms,omitempty"`
	Description         string         `json:"description,omitempty"`
	QueueID             int            `json:"queue_id,omitempty"`
	Parameters          map[string]any `json:"parameters,omitempty"`
	Causes              []Cause        `json:"causes,omitempty"`
	LogTail             []string       `json:"log_tail,omitempty"`
}

// Done reports whether the build has a final result.
func (b *Build) Done() bool { return !b.Building && b.Result != "" }

type rawBuild struct {
	Number            int     `json:"number"`
	URL               string  `json:"url"`
	Result            *string `json:"result"`
	Building          bool    `json:"building"`
	Timestamp         int64   `json:"timestamp"`
	Duration          int64   `json:"duration"`
	EstimatedDuration int64   `json:"estimatedDuration"`
	Description       *string `json:"description"`
	QueueID           int     `json:"queueId"`
	Actions           []struct {
		Parameters []struct {
			Name  string `json:"name"`
			Value any    `json:"value"`
		} `json:"parameters"`
		Causes []struct {
			ShortDescription string `json:"shortDescription"`
			UserID           string `json:"userId"`
		} `json:"causes"`
	} `json:"actions"`
}

func (r *rawBuild) toBuild(job string) *Build {
	b := &Build{
		Job: job, Number: r.Number, URL: r.URL, Building: r.Building,
		StartedAt:           time.UnixMilli(r.Timestamp).UTC(),
		DurationMs:          r.Duration,
		EstimatedDurationMs: r.EstimatedDuration,
		QueueID:             r.QueueID,
	}
	if r.Description != nil {
		b.Description = *r.Description
	}
	if r.Result != nil {
		b.Result = *r.Result
	}
	switch {
	case b.Building:
		b.Status = "RUNNING"
		// Jenkins reports duration 0 until the build finishes.
		b.DurationMs = time.Since(b.StartedAt).Milliseconds()
	case b.Result != "":
		b.Status = b.Result
	default:
		b.Status = "PENDING"
	}
	for _, a := range r.Actions {
		for _, p := range a.Parameters {
			if b.Parameters == nil {
				b.Parameters = map[string]any{}
			}
			b.Parameters[p.Name] = p.Value
		}
		for _, c := range a.Causes {
			b.Causes = append(b.Causes, Cause{Description: c.ShortDescription, UserID: c.UserID})
		}
	}
	return b
}

const buildTree = "number,url,result,building,timestamp,duration,estimatedDuration,description,queueId," +
	"actions[parameters[name,value],causes[shortDescription,userId]]"

func (c *Client) GetBuild(ctx context.Context, job string, number int) (*Build, error) {
	p, err := c.JobPath(job)
	if err != nil {
		return nil, err
	}
	name, _ := c.JobName(job)
	var raw rawBuild
	if err := c.GetJSON(ctx, fmt.Sprintf("%s/%d/api/json?tree=%s", p, number, url.QueryEscape(buildTree)), &raw); err != nil {
		return nil, err
	}
	return raw.toBuild(name), nil
}

// ListBuilds returns the newest builds first. Jenkins' "builds" field caps at
// 100, so larger limits fall back to the (more expensive) "allBuilds".
func (c *Client) ListBuilds(ctx context.Context, job string, limit int) ([]*Build, error) {
	p, err := c.JobPath(job)
	if err != nil {
		return nil, err
	}
	name, _ := c.JobName(job)
	field := "builds"
	if limit > 100 {
		field = "allBuilds"
	}
	tree := fmt.Sprintf("%s[%s]{0,%d}", field, buildTree, limit)
	var r struct {
		Builds    []rawBuild `json:"builds"`
		AllBuilds []rawBuild `json:"allBuilds"`
	}
	if err := c.GetJSON(ctx, p+"/api/json?tree="+url.QueryEscape(tree), &r); err != nil {
		return nil, err
	}
	raw := r.Builds
	if field == "allBuilds" {
		raw = r.AllBuilds
	}
	out := make([]*Build, 0, len(raw))
	for i := range raw {
		out = append(out, raw[i].toBuild(name))
	}
	return out, nil
}

// Trigger queues a build and returns the queue item id. Parameterized jobs
// must use buildWithParameters even when no values are supplied.
func (c *Client) Trigger(ctx context.Context, job string, params url.Values, parameterized bool) (int, error) {
	p, err := c.JobPath(job)
	if err != nil {
		return 0, err
	}
	endpoint := p + "/build"
	if parameterized || len(params) > 0 {
		endpoint = p + "/buildWithParameters"
	}
	resp, err := c.PostForm(ctx, endpoint, params)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	loc := resp.Header.Get("Location")
	id, ok := parseQueueID(loc)
	if !ok {
		return 0, fmt.Errorf("build triggered but Jenkins returned no queue location (HTTP %d)", resp.StatusCode)
	}
	return id, nil
}

func parseQueueID(loc string) (int, bool) {
	i := strings.Index(loc, "/queue/item/")
	if i < 0 {
		return 0, false
	}
	rest := strings.Trim(loc[i+len("/queue/item/"):], "/")
	id, err := strconv.Atoi(rest)
	return id, err == nil
}

type QueueItem struct {
	Why       string
	Cancelled bool
	BuildNum  int
}

func (c *Client) GetQueueItem(ctx context.Context, id int) (*QueueItem, error) {
	var r struct {
		Why        string `json:"why"`
		Cancelled  bool   `json:"cancelled"`
		Executable *struct {
			Number int `json:"number"`
		} `json:"executable"`
	}
	if err := c.GetJSON(ctx, fmt.Sprintf("/queue/item/%d/api/json", id), &r); err != nil {
		return nil, err
	}
	q := &QueueItem{Why: r.Why, Cancelled: r.Cancelled}
	if r.Executable != nil {
		q.BuildNum = r.Executable.Number
	}
	return q, nil
}

var ErrQueueCancelled = errors.New("queue item was cancelled before the build started")

// WaitForQueue polls until the queued item becomes a build. onPoll may be nil.
func (c *Client) WaitForQueue(ctx context.Context, id int, interval time.Duration, onPoll func(*QueueItem)) (*QueueItem, error) {
	for {
		q, err := c.GetQueueItem(ctx, id)
		if err != nil {
			return nil, err
		}
		if onPoll != nil {
			onPoll(q)
		}
		if q.Cancelled {
			return q, ErrQueueCancelled
		}
		if q.BuildNum > 0 {
			return q, nil
		}
		if err := Sleep(ctx, interval); err != nil {
			return q, err
		}
	}
}

// ProgressiveLog fetches console output from byte offset start. more is true
// while the build is still producing output.
func (c *Client) ProgressiveLog(ctx context.Context, job string, number int, start int64, w io.Writer) (next int64, more bool, err error) {
	p, err := c.JobPath(job)
	if err != nil {
		return 0, false, err
	}
	resp, err := c.Do(ctx, http.MethodGet, fmt.Sprintf("%s/%d/logText/progressiveText?start=%d", p, number, start), "", nil)
	if err != nil {
		return start, false, err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(w, resp.Body); err != nil {
		return start, false, err
	}
	next = start
	if v, err := strconv.ParseInt(resp.Header.Get("X-Text-Size"), 10, 64); err == nil {
		next = v
	}
	return next, resp.Header.Get("X-More-Data") == "true", nil
}

// LogTail streams the full console and keeps only the last n lines in memory.
func (c *Client) LogTail(ctx context.Context, job string, number, n int) ([]string, error) {
	p, err := c.JobPath(job)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(ctx, http.MethodGet, fmt.Sprintf("%s/%d/consoleText", p, number), "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	ring := make([]string, 0, n)
	br := bufio.NewReader(resp.Body)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			line = strings.TrimRight(line, "\r\n")
			if len(ring) == n {
				ring = append(ring[1:], line)
			} else {
				ring = append(ring, line)
			}
		}
		if err == io.EOF {
			return ring, nil
		}
		if err != nil {
			return ring, err
		}
	}
}

// Sleep waits for d or until ctx is done.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
