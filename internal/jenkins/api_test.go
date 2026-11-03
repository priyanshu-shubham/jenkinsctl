package jenkins

import (
	"errors"
	"testing"
)

func TestJobPath(t *testing.T) {
	c, err := New(Options{URL: "https://ci.example.com/jenkins/"})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"api":              "/job/api",
		"team/api/main":    "/job/team/job/api/job/main",
		"team/feature%2Fx": "/job/team/job/feature%252Fx",
		"team/a b":         "/job/team/job/a%20b",
		"https://ci.example.com/jenkins/job/team/job/api/job/main/42/console": "/job/team/job/api/job/main",
		"https://ci.example.com/jenkins/view/All/job/team/job/api/":           "/job/team/job/api",
	}
	for in, want := range cases {
		got, err := c.JobPath(in)
		if err != nil || got != want {
			t.Errorf("JobPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := c.JobPath("https://evil.example.com/job/x"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("foreign host should be rejected, got %v", err)
	}
}

func TestParseQueueID(t *testing.T) {
	if id, ok := parseQueueID("https://ci/queue/item/981/"); !ok || id != 981 {
		t.Fatalf("got %d %v", id, ok)
	}
	if _, ok := parseQueueID("https://ci/job/x/"); ok {
		t.Fatal("expected no id")
	}
}
