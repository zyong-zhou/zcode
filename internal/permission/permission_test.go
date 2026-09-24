package permission

import "testing"

func TestDecideDefaultAndOverrides(t *testing.T) {
	p := New("ask", map[string]string{"read_file": "allow", "write_file": "deny", "edit": "bogus"}, nil, nil)
	cases := []struct {
		tool string
		want Decision
	}{
		{"read_file", Allow},
		{"write_file", Deny},
		{"edit", Ask}, // unknown decision is dropped, falls back to default
		{"search", Ask},
	}
	for _, c := range cases {
		if got := p.Decide(c.tool, "{}"); got != c.want {
			t.Errorf("Decide(%q) = %q, want %q", c.tool, got, c.want)
		}
	}
}

func TestDecideZeroValueAllows(t *testing.T) {
	var p Policy
	if got := p.Decide("anything", "{}"); got != Allow {
		t.Errorf("zero policy = %q, want allow", got)
	}
}

func TestDecideCommandLists(t *testing.T) {
	p := New("ask", nil, []string{"go test", "gofmt"}, []string{"rm -rf"})

	cases := []struct {
		args string
		want Decision
	}{
		{`{"command":"go","args":["test","./..."]}`, Allow},
		{`{"command":"go","args":["test"]}`, Allow},
		{`{"command":"gofmt","args":["-w","."]}`, Allow},
		{`{"command":"rm","args":["-rf","/"]}`, Deny},
		{`{"command":"go","args":["build"]}`, Ask}, // not listed
		{`not json`, Ask},
	}
	for _, c := range cases {
		if got := p.Decide("run_command", c.args); got != c.want {
			t.Errorf("Decide(run_command, %s) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestDenyBeatsAllow(t *testing.T) {
	p := New("allow", nil, []string{"git"}, []string{"git push"})
	if got := p.Decide("run_command", `{"command":"git","args":["push"]}`); got != Deny {
		t.Errorf("deny should win, got %q", got)
	}
	if got := p.Decide("run_command", `{"command":"git","args":["status"]}`); got != Allow {
		t.Errorf("git status = %q, want allow", got)
	}
}
