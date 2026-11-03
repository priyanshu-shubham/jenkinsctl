package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeJenkins models one parameterized job "team/api" whose build #3 runs for
// `runPolls` polls and then finishes with `result`.
type fakeJenkins struct {
	t        *testing.T
	mu       sync.Mutex
	result   string
	runPolls int
	polls    int
	queued   int
	lastForm url.Values
	stopped  bool
	log      string
}

func newFake(t *testing.T, result string) (*fakeJenkins, *httptest.Server) {
	f := &fakeJenkins{t: t, result: result, runPolls: 2, log: "step 1\nstep 2\nfinal line\n"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeJenkins) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, p, ok := r.BasicAuth()
	if !ok || u != "alice" || p != "secret" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodPost && r.Header.Get("Jenkins-Crumb") != "c123" {
		http.Error(w, "No valid crumb", http.StatusForbidden)
		return
	}
	js := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	building := f.polls < f.runPolls

	switch path := r.URL.Path; {
	case path == "/me/api/json":
		js(map[string]any{"id": "alice", "fullName": "Alice"})
	case path == "/crumbIssuer/api/json":
		js(map[string]any{"crumbRequestField": "Jenkins-Crumb", "crumb": "c123"})
	case path == "/api/json":
		js(map[string]any{"_class": "hudson.model.Hudson", "jobs": []map[string]any{
			{"_class": "com.cloudbees.hudson.plugins.folder.Folder", "name": "team", "fullName": "team"},
		}})
	case path == "/job/team/api/json":
		js(map[string]any{"_class": "com.cloudbees.hudson.plugins.folder.Folder", "jobs": []map[string]any{
			{"_class": "org.jenkinsci.plugins.workflow.job.WorkflowJob", "fullName": "team/api"},
		}})
	case path == "/job/team/job/api/api/json":
		tree := r.URL.Query().Get("tree")
		if strings.HasPrefix(tree, "builds[") {
			js(map[string]any{"_class": "org.jenkinsci.plugins.workflow.job.WorkflowJob", "builds": []map[string]any{
				{"number": 2, "result": "FAILURE", "timestamp": time.Now().UnixMilli(), "duration": 1000},
				{"number": 1, "result": "SUCCESS", "timestamp": time.Now().UnixMilli(), "duration": 2000},
			}})
			return
		}
		js(map[string]any{
			"_class": "org.jenkinsci.plugins.workflow.job.WorkflowJob", "name": "api", "fullName": "team/api",
			"buildable": true, "color": "red", "lastBuild": map[string]any{"number": 3},
			"property": []map[string]any{{"parameterDefinitions": []map[string]any{
				{"name": "BRANCH", "type": "StringParameterDefinition", "defaultParameterValue": map[string]any{"value": "main"}},
				{"name": "ENV", "type": "ChoiceParameterDefinition", "choices": []string{"dev", "prod"}},
			}}},
		})
	case path == "/job/team/job/api/buildWithParameters":
		_ = r.ParseForm()
		f.lastForm = r.PostForm
		w.Header().Set("Location", "http://"+r.Host+"/queue/item/7/")
		w.WriteHeader(http.StatusCreated)
	case path == "/queue/item/7/api/json":
		f.queued++
		if f.queued < 2 {
			js(map[string]any{"_class": "hudson.model.Queue$WaitingItem", "id": 7, "why": "Waiting for next available executor"})
			return
		}
		js(map[string]any{"id": 7, "executable": map[string]any{"number": 3, "url": "http://x/job/team/job/api/3/"}})
	case path == "/job/team/job/api/3/api/json", path == "/job/team/job/api/lastBuild/api/json":
		f.polls++
		b := map[string]any{"_class": "org.jenkinsci.plugins.workflow.job.WorkflowRun", "number": 3, "building": building, "timestamp": time.Now().UnixMilli(),
			"actions": []map[string]any{{}, {"parameters": []map[string]any{{"name": "BRANCH", "value": "main"}}}}}
		if !building {
			b["result"] = f.result
		}
		js(b)
	case path == "/job/team/job/api/3/logText/progressiveText":
		// The run advances on log reads too, and its log grows: only the first
		// line exists until it finishes.
		f.polls++
		text := f.log
		if building {
			text = f.log[:strings.Index(f.log, "\n")+1]
			w.Header().Set("X-More-Data", "true")
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		w.Header().Set("X-Text-Size", strconv.Itoa(len(text)))
		if start < len(text) {
			fmt.Fprint(w, text[start:])
		}
	case path == "/job/team/job/api/3/stop" && r.Method == http.MethodPost:
		f.stopped = true
		http.Redirect(w, r, "/job/team/job/api/3/", http.StatusFound)
	case path == "/job/team/job/api/3/":
		fmt.Fprint(w, "<html>run page</html>")
	case path == "/queue/cancelItem" && r.Method == http.MethodPost:
		_ = r.ParseForm()
		f.lastForm = r.PostForm
		w.WriteHeader(http.StatusNoContent)
	case path == "/job/team/job/api/3/consoleText":
		fmt.Fprint(w, f.log)
	default:
		http.NotFound(w, r)
	}
}

// useServer points JENKINSCTL_CONFIG at a temp config holding server "prod" for srvURL.
func useServer(t *testing.T, srvURL, token string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("JENKINSCTL_CONFIG", p)
	cfg := fmt.Sprintf(`{"servers":{"prod":{"url":%q,"user":"alice","token":%q}}}`, srvURL, token)
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runCLIIn(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), append([]string{"-o", "json"}, args...), strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return runCLIIn(t, "", args...)
}

type result struct {
	Number   int            `json:"number"`
	Status   string         `json:"status"`
	QueueID  int            `json:"queue_id"`
	LogTail  []string       `json:"log_tail"`
	Params   map[string]any `json:"parameters"`
	Error    *errInfo       `json:"error"`
	FullName string         `json:"full_name"`
}

// decode asserts stdout is exactly one compact JSON line and parses it.
func decode(t *testing.T, out string) result {
	t.Helper()
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("stdout must be one JSON line, got %q", out)
	}
	var r result
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	return r
}

func TestSubmitWatchFailureReportsRunFailedWithLog(t *testing.T) {
	f, srv := newFake(t, "FAILURE")
	useServer(t, srv.URL, "secret")
	code, out, stderr := runCLI(t, "runs", "submit", "-s", "prod", "team/api", "-p", "ENV=prod", "--watch", "--interval", "1ms")
	r := decode(t, out)
	if code != ExitError || r.Error == nil || r.Error.Kind != kindRunFailed {
		t.Fatalf("exit=%d out=%s stderr=%s", code, out, stderr)
	}
	if r.Number != 3 || r.Status != "FAILURE" || len(r.LogTail) != 3 {
		t.Fatalf("failed run should carry its state and default log tail: %+v", r)
	}
	if stderr != "" {
		t.Fatalf("JSON mode should keep stderr quiet: %q", stderr)
	}
	if got := f.lastForm["ENV"]; len(got) != 1 || got[0] != "prod" {
		t.Fatalf("ENV not submitted: %v", f.lastForm)
	}
}

func TestSuccessHasNoErrorAndNoDefaultTail(t *testing.T) {
	_, srv := newFake(t, "SUCCESS")
	useServer(t, srv.URL, "secret")
	code, out, _ := runCLI(t, "runs", "watch", "-s", "prod", "team/api", "3", "--interval", "1ms")
	r := decode(t, out)
	if code != ExitOK || r.Error != nil || r.Status != "SUCCESS" || r.LogTail != nil || strings.Contains(out, `"result"`) {
		t.Fatalf("exit=%d out=%s", code, out)
	}
}

func TestSubmitReturnsBuildNumberWithoutWaitingForResult(t *testing.T) {
	f, srv := newFake(t, "SUCCESS")
	useServer(t, srv.URL, "secret")
	f.runPolls = 1 << 30
	code, out, _ := runCLI(t, "runs", "submit", "-s", "prod", "team/api", "--interval", "1ms")
	if r := decode(t, out); code != ExitOK || r.Number != 3 || r.Status != "RUNNING" || r.Error != nil {
		t.Fatalf("exit=%d out=%s", code, out)
	}
}

// runResume executes an error's resume command verbatim, minus the binary name.
func runResume(t *testing.T, resume string, extra ...string) (int, string) {
	t.Helper()
	args := strings.Fields(resume)
	if args[0] != "jenkinsctl" {
		t.Fatalf("resume should start with jenkinsctl: %q", resume)
	}
	code, out, _ := runCLI(t, append(args[1:], extra...)...)
	return code, out
}

func TestQueueTimeoutResumes(t *testing.T) {
	f, srv := newFake(t, "SUCCESS")
	useServer(t, srv.URL, "secret")
	f.queued = -1 << 30
	code, out, _ := runCLI(t, "runs", "submit", "-s", "prod", "team/api", "--watch", "--interval", "10ms", "--timeout", "30ms")
	r := decode(t, out)
	if code != ExitError || r.Error == nil || r.Error.Kind != kindTimeout || r.Status != "QUEUED" || r.QueueID != 7 {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	want := "jenkinsctl runs watch -s prod team/api --queue 7 --timeout 30ms"
	if r.Error.Resume != want {
		t.Fatalf("resume = %q, want %q", r.Error.Resume, want)
	}
	f.queued = 0
	code, out = runResume(t, strings.Replace(r.Error.Resume, "30ms", "1m", 1), "--interval", "1ms")
	if r := decode(t, out); code != ExitOK || r.Status != "SUCCESS" {
		t.Fatalf("resume: exit=%d out=%s", code, out)
	}
}

func TestWatchTimeoutResumes(t *testing.T) {
	f, srv := newFake(t, "SUCCESS")
	useServer(t, srv.URL, "secret")
	f.runPolls = 1 << 30
	code, out, _ := runCLI(t, "runs", "watch", "-s", "prod", "team/api", "3", "--interval", "20ms", "--timeout", "50ms")
	r := decode(t, out)
	if code != ExitError || r.Error == nil || r.Error.Kind != kindTimeout || r.Status != "RUNNING" || r.LogTail != nil {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	if want := "jenkinsctl runs watch -s prod team/api 3 --timeout 50ms"; r.Error.Resume != want {
		t.Fatalf("resume = %q, want %q", r.Error.Resume, want)
	}
	f.runPolls = 0
	if code, out := runResume(t, r.Error.Resume, "--interval", "1ms"); code != ExitOK {
		t.Fatalf("resume: exit=%d out=%s", code, out)
	}
}

func TestDefaultTimeoutFitsClaudeCodeBashLimit(t *testing.T) {
	if defaultTimeout >= 2*time.Minute {
		t.Fatalf("default timeout %s must stay under the 2m Bash tool limit", defaultTimeout)
	}
}

func TestShellQuoteInResume(t *testing.T) {
	o := watchOpts{server: "prod", job: "team/my job", timeout: time.Minute}
	if got := o.resume(4, 0); got != "jenkinsctl runs watch -s prod 'team/my job' 4 --timeout 1m0s" {
		t.Fatalf("got %q", got)
	}
}

func TestSubmitRejectsBadInputBeforeTriggering(t *testing.T) {
	f, srv := newFake(t, "SUCCESS")
	useServer(t, srv.URL, "secret")
	for _, c := range []struct {
		args []string
		kind string
	}{
		{[]string{"-p", "BRNCH=x"}, kindInvalidParam},
		{[]string{"-p", "ENV=qa"}, kindInvalidParam},
		{[]string{"-p", "novalue"}, kindUsage},
		{[]string{"--watch", "--timeout", "-1s"}, kindUsage},
		{[]string{"--logs"}, kindUsage}, // removed flag
	} {
		code, out, _ := runCLI(t, append([]string{"runs", "submit", "-s", "prod", "team/api"}, c.args...)...)
		if r := decode(t, out); code != ExitError || r.Error == nil || r.Error.Kind != c.kind {
			t.Errorf("%v: exit=%d out=%s", c.args, code, out)
		}
	}
	if f.lastForm != nil {
		t.Fatal("a rejected submit must not trigger a build")
	}
	_, out, _ := runCLI(t, "runs", "submit", "-s", "prod", "team/api", "-p", "BRNCH=x")
	if r := decode(t, out); strings.Join(r.Error.Valid, ",") != "BRANCH,ENV" {
		t.Fatalf("error should list valid params: %s", out)
	}
	_, out, _ = runCLI(t, "runs", "submit", "-s", "prod", "team/api", "-p", "ENV=qa")
	if r := decode(t, out); strings.Join(r.Error.Valid, ",") != "dev,prod" {
		t.Fatalf("error should list valid choices: %s", out)
	}
}

func TestLogs(t *testing.T) {
	const full = "step 1\nstep 2\nfinal line\n"
	cases := []struct {
		name     string
		runPolls int
		args     []string
		out      string
		note     string
	}{
		{"finished run", 0, nil, full, "finished with FAILURE"},
		{"tail", 0, []string{"--tail", "2"}, "step 2\nfinal line\n", "finished with FAILURE"},
		{"running snapshot exits at once", 1 << 30, nil, "step 1\n", "still running"},
		{"follow until finished", 3, []string{"--follow"}, full, "finished with FAILURE"},
		{"tail then follow", 0, []string{"--tail", "1", "--follow"}, "final line\n", "finished with FAILURE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFake(t, "FAILURE")
			useServer(t, srv.URL, "secret")
			f.runPolls = c.runPolls
			code, out, stderr := runCLI(t, append([]string{"runs", "logs", "-s", "prod", "team/api", "3", "--interval", "1ms"}, c.args...)...)
			if code != ExitOK || out != c.out || !strings.Contains(stderr, c.note) {
				t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
			}
		})
	}
}

func TestLogsMissingRunIsExplained(t *testing.T) {
	_, srv := newFake(t, "SUCCESS")
	useServer(t, srv.URL, "secret")
	_, out, _ := runCLI(t, "runs", "logs", "-s", "prod", "team/api", "99")
	if r := decode(t, out); r.Error.Kind != kindNotFound || !strings.Contains(r.Error.Message, "latest is #3") {
		t.Fatalf("out=%s", out)
	}
}

func TestInterruptExitsQuietly(t *testing.T) {
	_, srv := newFake(t, "SUCCESS")
	useServer(t, srv.URL, "secret")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errb bytes.Buffer
	code := run(ctx, []string{"runs", "logs", "-s", "prod", "team/api", "3", "--follow"}, nil, &out, &errb)
	if code != ExitInterrupted || out.Len() != 0 || errb.Len() != 0 {
		t.Fatalf("exit=%d out=%q err=%q", code, out.String(), errb.String())
	}
}

func TestTailWriterAcrossChunks(t *testing.T) {
	tw := &tailWriter{n: 2}
	for _, chunk := range []string{"a\nb", "\nc\n", "d"} {
		_, _ = tw.Write([]byte(chunk))
	}
	var buf bytes.Buffer
	_ = tw.flush(&buf)
	if buf.String() != "b\nc\nd" {
		t.Fatalf("got %q", buf.String())
	}
}

func TestListRuns(t *testing.T) {
	_, srv := newFake(t, "SUCCESS")
	useServer(t, srv.URL, "secret")
	code, out, _ := runCLI(t, "runs", "list", "-s", "prod", "team/api")
	var builds []map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &builds) != nil || len(builds) != 2 || builds[0]["status"] != "FAILURE" {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	var txt, errb bytes.Buffer
	code = run(context.Background(), []string{"-o", "text", "runs", "list", "-s", "prod", "team/api"}, nil, &txt, &errb)
	if code != 0 || !strings.HasPrefix(txt.String(), "#") {
		t.Fatalf("text mode: exit=%d out=%s", code, txt.String())
	}
}

func TestTextModeErrorsGoToStderrWithResume(t *testing.T) {
	f, srv := newFake(t, "SUCCESS")
	useServer(t, srv.URL, "secret")
	f.runPolls = 1 << 30
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"-o", "text", "runs", "watch", "-s", "prod", "team/api", "3", "--interval", "5ms", "--timeout", "20ms"}, nil, &out, &errb)
	if code != ExitError || !strings.Contains(out.String(), "RUNNING") || !strings.Contains(errb.String(), "resume: jenkinsctl runs watch -s prod team/api 3") {
		t.Fatalf("exit=%d out=%s err=%s", code, out.String(), errb.String())
	}
}

func TestJobsShow(t *testing.T) {
	_, srv := newFake(t, "SUCCESS")
	useServer(t, srv.URL, "secret")
	code, out, _ := runCLI(t, "jobs", "show", "-s", "prod", "team/api")
	var j struct {
		FullName string `json:"full_name"`
		Params   []struct {
			Name    string   `json:"name"`
			Choices []string `json:"choices"`
		} `json:"parameters"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &j) != nil || j.FullName != "team/api" || len(j.Params) != 2 || len(j.Params[1].Choices) != 2 {
		t.Fatalf("exit=%d out=%s", code, out)
	}
}

func TestErrorKinds(t *testing.T) {
	_, srv := newFake(t, "SUCCESS")
	useServer(t, srv.URL, "secret")
	cases := []struct {
		args []string
		kind string
		msg  string
	}{
		{[]string{"runs", "list", "team/api"}, kindUsage, "--server is required (configured: [prod])"},
		{[]string{"runs", "list", "-s", "nope", "team/api"}, kindUsage, `unknown server "nope"`},
		{[]string{"runs", "list", "-s", "prod", "team/missing"}, kindNotFound, ""},
		{[]string{"runs", "watch", "-s", "prod", "team/api", "last"}, kindUsage, "positive build number"},
		{[]string{"runs", "watch", "-s", "prod", "team/api"}, kindUsage, ""},
		{[]string{"runs", "bogus"}, kindUsage, ""},
		{[]string{"runs", "trigger"}, kindUsage, ""},
		{[]string{"completion"}, kindUsage, ""},
		{[]string{"runs", "list", "-s", "prod", "--nope"}, kindUsage, ""},
	}
	for _, c := range cases {
		code, out, stderr := runCLI(t, c.args...)
		r := decode(t, out)
		if code != ExitError || r.Error == nil || r.Error.Kind != c.kind || !strings.Contains(r.Error.Message, c.msg) || stderr != "" {
			t.Errorf("%v: exit=%d out=%s stderr=%s", c.args, code, out, stderr)
		}
	}
	useServer(t, srv.URL, "wrong")
	_, out, _ := runCLI(t, "runs", "list", "-s", "prod", "team/api")
	if r := decode(t, out); r.Error.Kind != kindAuth || r.Error.HTTPStatus != 401 {
		t.Fatalf("bad token: %s", out)
	}
	useServer(t, "http://127.0.0.1:1", "secret")
	_, out, _ = runCLI(t, "runs", "list", "-s", "prod", "team/api")
	if r := decode(t, out); r.Error.Kind != kindNetwork {
		t.Fatalf("unreachable server: %s", out)
	}
}

func TestAuthLoginInteractiveFlow(t *testing.T) {
	_, srv := newFake(t, "SUCCESS")
	t.Setenv("JENKINSCTL_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	answers := func(name, token string) string {
		return strings.Join([]string{name, srv.URL, "alice", token}, "\n") + "\n"
	}

	if code, _, stderr := runCLIIn(t, answers("prod", "secret"), "auth", "login"); code != 0 {
		t.Fatalf("login prod: exit=%d %s", code, stderr)
	}
	if code, _, stderr := runCLIIn(t, answers("staging", "secret"), "auth", "login"); code != 0 {
		t.Fatalf("login staging: exit=%d %s", code, stderr)
	}
	if _, out, _ := runCLIIn(t, answers("bad", "nope"), "auth", "login"); decode(t, out).Error.Kind != kindAuth {
		t.Fatalf("bad token should fail with auth: %s", out)
	}
	// Replacing an existing server needs confirmation; "n" leaves it untouched.
	if code, _, _ := runCLIIn(t, "prod\nn\n", "auth", "login"); code != ExitError {
		t.Fatalf("declined overwrite exit=%d", code)
	}

	code, out, _ := runCLI(t, "auth", "list")
	if code != 0 || strings.Contains(out, "secret") {
		t.Fatalf("auth list: exit=%d out=%s", code, out)
	}
	var servers []struct{ Name string }
	_ = json.Unmarshal([]byte(out), &servers)
	if len(servers) != 2 || servers[0].Name != "prod" || servers[1].Name != "staging" {
		t.Fatalf("unexpected servers: %s", out)
	}

	if code, _, _ := runCLI(t, "runs", "list", "-s", "staging", "team/api"); code != 0 {
		t.Fatalf("staging should be usable, exit=%d", code)
	}
	if code, _, _ := runCLI(t, "auth", "logout", "-s", "staging"); code != 0 {
		t.Fatal("logout failed")
	}
	if code, _, _ := runCLI(t, "runs", "list", "-s", "staging", "team/api"); code != ExitError {
		t.Fatalf("logged-out server should be unknown, exit=%d", code)
	}
}

func TestAuthLoginNeedsAnswers(t *testing.T) {
	t.Setenv("JENKINSCTL_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	_, out, _ := runCLIIn(t, "", "auth", "login")
	if r := decode(t, out); r.Error.Kind != kindUsage || !strings.Contains(r.Error.Message, "a human must run") {
		t.Fatalf("out=%s", out)
	}
}

func TestJobAcceptsURLPathFragments(t *testing.T) {
	_, srv := newFake(t, "SUCCESS")
	useServer(t, srv.URL, "secret")
	for _, job := range []string{"team/job/api", "job/team/job/api", "/job/team/job/api/"} {
		code, out, _ := runCLI(t, "runs", "watch", "-s", "prod", job, "3", "--interval", "1ms")
		if r := decode(t, out); code != ExitOK || r.Number != 3 {
			t.Errorf("%s: exit=%d out=%s", job, code, out)
		}
	}
}

func TestNotFoundSaysWhatIsMissing(t *testing.T) {
	_, srv := newFake(t, "SUCCESS")
	useServer(t, srv.URL, "secret")

	_, out, _ := runCLI(t, "runs", "watch", "-s", "prod", "team/api", "99")
	r := decode(t, out)
	if r.Error.Kind != kindNotFound || r.Error.Message != "job team/api has no run #99; the latest is #3" {
		t.Fatalf("missing run: %s", out)
	}

	_, out, _ = runCLI(t, "jobs", "show", "-s", "prod", "team/apx")
	r = decode(t, out)
	if r.Error.Kind != kindNotFound || strings.Join(r.Error.Valid, ",") != "team/api" || !strings.Contains(r.Error.Message, "folder team") {
		t.Fatalf("missing job should list siblings: %s", out)
	}
	if strings.Contains(r.Error.Message, "tree=") {
		t.Fatalf("message should not include the API query: %s", out)
	}

	// A missing folder falls back to the nearest folder that exists.
	_, out, _ = runCLI(t, "runs", "list", "-s", "prod", "tem/api")
	r = decode(t, out)
	if strings.Join(r.Error.Valid, ",") != "team" || !strings.Contains(r.Error.Message, "the top level") {
		t.Fatalf("missing folder: %s", out)
	}
}

func TestAPI(t *testing.T) {
	f, srv := newFake(t, "SUCCESS")
	useServer(t, srv.URL, "secret")

	// GET passes the body through unchanged.
	code, out, _ := runCLI(t, "api", "-s", "prod", "/job/team/job/api/api/json?tree=builds[number]")
	if code != ExitOK || !strings.Contains(out, `"builds"`) {
		t.Fatalf("GET: exit=%d out=%s", code, out)
	}

	// POST gets a crumb (the fake rejects it otherwise) and reports the
	// redirect instead of following it to an HTML page.
	code, out, _ = runCLI(t, "api", "-s", "prod", "-X", "post", "/job/team/job/api/3/stop")
	if code != ExitOK || !f.stopped || out != `{"http_status":302,"location":"/job/team/job/api/3/"}`+"\n" {
		t.Fatalf("POST stop: exit=%d out=%q", code, out)
	}

	// --data implies POST and is form-encoded.
	code, out, _ = runCLI(t, "api", "-s", "prod", "/queue/cancelItem", "-d", "id=981")
	if code != ExitOK || f.lastForm.Get("id") != "981" || !strings.Contains(out, `"http_status":204`) {
		t.Fatalf("POST data: exit=%d out=%q form=%v", code, out, f.lastForm)
	}

	_, out, _ = runCLI(t, "api", "-s", "prod", "/nope")
	if r := decode(t, out); r.Error.Kind != kindNotFound || r.Error.HTTPStatus != 404 {
		t.Fatalf("404: %s", out)
	}
	_, out, _ = runCLI(t, "api", "/queue/api/json")
	if r := decode(t, out); r.Error.Kind != kindUsage {
		t.Fatalf("missing -s: %s", out)
	}
	_, out, _ = runCLI(t, "api", "-s", "prod", "https://evil.example.com/x")
	if r := decode(t, out); r.Error.Kind != kindUsage {
		t.Fatalf("foreign host must be rejected: %s", out)
	}
}
