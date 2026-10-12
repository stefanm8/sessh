package bitbucket

import (
	"reflect"
	"testing"
)

func intPtr(n int) *int { return &n }

// pipeline builds a Pipeline without the deep struct-literal noise the anonymous
// nested types would otherwise force on every test case.
type pipelineSpec struct {
	uuid        string
	buildNumber *int
	stateName   string
	resultName  string
	pattern     string
	refName     string
	branch      string
	commit      string
	href        string
	createdOn   string
	completedOn string
}

func (s pipelineSpec) build() Pipeline {
	var p Pipeline
	p.UUID = s.uuid
	p.BuildNumber = s.buildNumber
	p.State.Name = s.stateName
	p.State.Result.Name = s.resultName
	p.Target.Selector.Pattern = s.pattern
	p.Target.RefName = s.refName
	p.Target.Branch.Name = s.branch
	p.Target.Commit.Hash = s.commit
	p.Links.HTML.Href = s.href
	p.CreatedOn = s.createdOn
	p.CompletedOn = s.completedOn
	return p
}

func status(key, name, state, url, commit, created, updated string) Status {
	var st Status
	st.Key, st.Name, st.State, st.URL = key, name, state, url
	st.Commit.Hash = commit
	st.CreatedOn, st.UpdatedOn = created, updated
	return st
}

func TestResolveStates(t *testing.T) {
	tests := []struct {
		name string
		flag string
		want []string
	}{
		{"an unset flag means open", "", []string{"OPEN"}},
		{"open is open", "open", []string{"OPEN"}},
		{"open is case insensitive", "OPEN", []string{"OPEN"}},
		{"all expands to every state", "all", []string{"OPEN", "MERGED", "DECLINED", "SUPERSEDED"}},
		{"all is case insensitive", "ALL", []string{"OPEN", "MERGED", "DECLINED", "SUPERSEDED"}},
		{"closed is everything except open", "closed", []string{"MERGED", "DECLINED", "SUPERSEDED"}},
		{"a specific state is upper-cased", "merged", []string{"MERGED"}},
		{"declined passes through", "declined", []string{"DECLINED"}},
		{"superseded passes through", "superseded", []string{"SUPERSEDED"}},
		{"whitespace around a keyword is tolerated", "  all  ", []string{"OPEN", "MERGED", "DECLINED", "SUPERSEDED"}},
		{"an unknown state is passed on rather than silently dropped", "bogus", []string{"BOGUS"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveStates(tt.flag); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveStates(%q) = %v, want %v", tt.flag, got, tt.want)
			}
		})
	}

	t.Run("whitespace around a concrete state is trimmed too", func(t *testing.T) {
		if got := resolveStates("  merged  "); !reflect.DeepEqual(got, []string{"MERGED"}) {
			t.Errorf("resolveStates(%q) = %q, want [MERGED]", "  merged  ", got)
		}
	})
}

func TestPipelineState(t *testing.T) {
	for _, tt := range []struct{ state, result, want string }{
		{"COMPLETED", "SUCCESSFUL", "SUCCESSFUL"},
		{"COMPLETED", "FAILED", "FAILED"},
		{"IN_PROGRESS", "", "IN_PROGRESS"},
		{"COMPLETED", "", "COMPLETED"},
		{"completed", "successful", "SUCCESSFUL"},
		{"", "", "UNKNOWN"},
		{"PENDING", "SUCCESSFUL", "PENDING"},
	} {
		t.Run(tt.state+"/"+tt.result, func(t *testing.T) {
			p := pipelineSpec{stateName: tt.state, resultName: tt.result}.build()
			if got := PipelineState(p); got != tt.want {
				t.Errorf("PipelineState() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckOutcome(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  string
	}{
		{"SUCCESSFUL is a pass", "SUCCESSFUL", "successful"},
		{"SUCCESS is a pass", "SUCCESS", "successful"},
		{"PASSED is a pass", "PASSED", "successful"},
		{"lower case is normalized", "successful", "successful"},
		{"INPROGRESS is pending", "INPROGRESS", "pending"},
		{"IN_PROGRESS is pending", "IN_PROGRESS", "pending"},
		{"PENDING is pending", "PENDING", "pending"},
		{"RUNNING is pending", "RUNNING", "pending"},
		{"PAUSED is pending", "PAUSED", "pending"},
		{"NOT_RUN is pending", "NOT_RUN", "pending"},
		{"FAILED is a failure", "FAILED", "failed"},
		{"ERROR is a failure", "ERROR", "failed"},
		{"STOPPED is a terminal failure, not pending", "STOPPED", "failed"},
		{"an unknown state fails closed", "UNKNOWN", "failed"},
		{"an empty state fails closed", "", "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CheckOutcome(tt.state); got != tt.want {
				t.Errorf("CheckOutcome(%q) = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

func TestCheckCountsAndExitCode(t *testing.T) {
	for _, tt := range []struct {
		name   string
		states []string
		counts [3]int // successful, pending, failed
		exit   int
	}{
		{"no checks", nil, [3]int{}, 1},
		{"all green", []string{"SUCCESSFUL", "SUCCESS"}, [3]int{2, 0, 0}, 0},
		{"mixed", []string{"SUCCESSFUL", "IN_PROGRESS", "FAILED", "STOPPED"}, [3]int{1, 1, 2}, 1},
		{"pending only", []string{"PENDING", "RUNNING"}, [3]int{0, 2, 0}, 8},
		{"one pass", []string{"SUCCESSFUL"}, [3]int{1, 0, 0}, 0},
		{"several passes", []string{"SUCCESSFUL", "SUCCESS", "PASSED"}, [3]int{3, 0, 0}, 0},
		{"still running", []string{"IN_PROGRESS"}, [3]int{0, 1, 0}, 8},
		{"failure outranks pending", []string{"IN_PROGRESS", "FAILED"}, [3]int{0, 1, 1}, 1},
		{"failure outranks success", []string{"SUCCESSFUL", "FAILED"}, [3]int{1, 0, 1}, 1},
		{"pending outranks success", []string{"SUCCESSFUL", "PENDING"}, [3]int{1, 1, 0}, 8},
		{"unknown fails closed", []string{"UNKNOWN"}, [3]int{0, 0, 1}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checks := make([]Check, len(tt.states))
			for i, state := range tt.states {
				checks[i].State = state
			}
			ok, pending, failed := CheckCounts(checks)
			if got := [3]int{ok, pending, failed}; got != tt.counts {
				t.Errorf("CheckCounts(%v) = %v, want %v", tt.states, got, tt.counts)
			}
			if got := ChecksExitCode(checks); got != tt.exit {
				t.Errorf("ChecksExitCode(%v) = %d, want %d", tt.states, got, tt.exit)
			}
		})
	}
}

func TestCombineChecks(t *testing.T) {
	t.Run("statuses and native pipelines collapse into one shape", func(t *testing.T) {
		got := CombineChecks(
			[]Status{status("build-1", "Azure build", "FAILED",
				"https://ci.example/build/1", "abc", "", "2026-01-02T00:00:00Z")},
			[]Pipeline{pipelineSpec{
				uuid: "{pipeline-1}", buildNumber: intPtr(12),
				stateName: "COMPLETED", resultName: "SUCCESSFUL",
				pattern: "default", commit: "abc",
				href:      "https://bitbucket.org/pipeline/12",
				createdOn: "2026-01-01T00:00:00Z",
			}.build()},
		)
		if len(got) != 2 {
			t.Fatalf("CombineChecks() returned %d checks, want 2", len(got))
		}
		type triple struct{ typ, name, state string }
		want := []triple{
			{"status", "Azure build", "FAILED"},
			{"pipeline", "default #12", "SUCCESSFUL"},
		}
		for i, w := range want {
			g := triple{got[i].Type, got[i].Name, got[i].State}
			if g != w {
				t.Errorf("check[%d] = %+v, want %+v", i, g, w)
			}
		}
		if got[0].Key != "build-1" || got[0].Commit != "abc" || got[0].UpdatedOn != "2026-01-02T00:00:00Z" {
			t.Errorf("status check = %+v", got[0])
		}
		if got[1].Key != "{pipeline-1}" || got[1].Commit != "abc" {
			t.Errorf("pipeline check = %+v", got[1])
		}
	})

	for _, tt := range []struct {
		name, statusURL, pipelineURL string
		count                        int
	}{
		{"published status suppresses pipeline", "https://ci.example/run/1/", "https://ci.example/run/1", 1},
		{"different URLs are kept", "https://ci.example/run/1", "https://ci.example/run/2", 2},
		{"URL-less runs are kept", "", "", 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := CombineChecks(
				[]Status{status("k", "Pipeline", "SUCCESSFUL", tt.statusURL, "", "", "")},
				[]Pipeline{pipelineSpec{buildNumber: intPtr(1), stateName: "COMPLETED", resultName: "SUCCESSFUL", href: tt.pipelineURL}.build()},
			)
			if len(got) != tt.count || got[0].Type != "status" {
				t.Fatalf("checks = %+v, want %d with status first", got, tt.count)
			}
		})
	}

	t.Run("newest first, by updated time then created time", func(t *testing.T) {
		got := CombineChecks(
			[]Status{
				status("old", "Old", "SUCCESSFUL", "", "", "", "2026-01-01T00:00:00Z"),
				status("new", "New", "SUCCESSFUL", "", "", "", "2026-03-01T00:00:00Z"),
				status("mid", "Mid", "SUCCESSFUL", "", "", "2026-02-01T00:00:00Z", ""),
			},
			nil,
		)
		var names []string
		for _, c := range got {
			names = append(names, c.Name)
		}
		want := []string{"New", "Mid", "Old"}
		if !reflect.DeepEqual(names, want) {
			t.Errorf("order = %v, want %v", names, want)
		}
	})

	t.Run("a check with no usable timestamp sorts last", func(t *testing.T) {
		got := CombineChecks(
			[]Status{
				status("undated", "Undated", "SUCCESSFUL", "", "", "", ""),
				status("dated", "Dated", "SUCCESSFUL", "", "", "", "2020-01-01T00:00:00Z"),
			},
			nil,
		)
		if got[0].Name != "Dated" || got[1].Name != "Undated" {
			t.Errorf("order = [%s %s], want [Dated Undated]", got[0].Name, got[1].Name)
		}
	})

	t.Run("equal timestamps keep their input order", func(t *testing.T) {
		ts := "2026-01-01T00:00:00Z"
		got := CombineChecks(
			[]Status{
				status("a", "A", "SUCCESSFUL", "", "", "", ts),
				status("b", "B", "SUCCESSFUL", "", "", "", ts),
				status("c", "C", "SUCCESSFUL", "", "", "", ts),
			},
			nil,
		)
		var names []string
		for _, c := range got {
			names = append(names, c.Name)
		}
		if want := []string{"A", "B", "C"}; !reflect.DeepEqual(names, want) {
			t.Errorf("order = %v, want stable %v", names, want)
		}
	})

	t.Run("empty inputs yield an empty, non-nil list", func(t *testing.T) {
		got := CombineChecks(nil, nil)
		if got == nil {
			t.Fatal("CombineChecks(nil, nil) = nil, want an empty slice so --json emits []")
		}
		if len(got) != 0 {
			t.Errorf("CombineChecks(nil, nil) = %v, want empty", got)
		}
	})

	t.Run("status naming falls back from name to key to a generic label", func(t *testing.T) {
		tests := []struct {
			name     string
			key, nam string
			want     string
		}{
			{"the reported name wins", "k", "Azure build", "Azure build"},
			{"the key is used when there is no name", "build-key", "", "build-key"},
			{"neither means a generic label", "", "", "Build status"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := CombineChecks([]Status{status(tt.key, tt.nam, "SUCCESSFUL", "", "", "", "")}, nil)
				if got[0].Name != tt.want {
					t.Errorf("Name = %q, want %q", got[0].Name, tt.want)
				}
			})
		}
	})

	t.Run("a status with no state is UNKNOWN, which counts as a failure", func(t *testing.T) {
		got := CombineChecks([]Status{status("k", "K", "", "", "", "", "")}, nil)
		if got[0].State != "UNKNOWN" {
			t.Errorf("State = %q, want UNKNOWN", got[0].State)
		}
		if ChecksExitCode(got) != 1 {
			t.Errorf("ChecksExitCode = %d, want 1 for an unknown state", ChecksExitCode(got))
		}
	})

	t.Run("pipeline naming uses the selector pattern and build number", func(t *testing.T) {
		tests := []struct {
			name string
			spec pipelineSpec
			want string
		}{
			{"pattern and number", pipelineSpec{pattern: "default", buildNumber: intPtr(12)}, "default #12"},
			{"pattern only", pipelineSpec{pattern: "custom:deploy"}, "custom:deploy"},
			{"number only", pipelineSpec{buildNumber: intPtr(7)}, "Pipeline #7"},
			{"neither", pipelineSpec{}, "Pipeline"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := CombineChecks(nil, []Pipeline{tt.spec.build()})
				if got[0].Name != tt.want {
					t.Errorf("Name = %q, want %q", got[0].Name, tt.want)
				}
			})
		}
	})

	for _, tt := range []struct {
		name                      string
		spec                      pipelineSpec
		key, updated, description string
	}{
		{name: "key uses build number", spec: pipelineSpec{buildNumber: intPtr(42)}, key: "42"},
		{name: "uuid wins", spec: pipelineSpec{uuid: "{u}", buildNumber: intPtr(42)}, key: "{u}"},
		{name: "running uses creation time", spec: pipelineSpec{stateName: "IN_PROGRESS", createdOn: "2026-01-01T00:00:00Z"}, updated: "2026-01-01T00:00:00Z"},
		{name: "finished uses completion time", spec: pipelineSpec{stateName: "COMPLETED", resultName: "SUCCESSFUL", createdOn: "2026-01-01T00:00:00Z", completedOn: "2026-01-01T00:10:00Z"}, updated: "2026-01-01T00:10:00Z"},
		{name: "description uses ref", spec: pipelineSpec{refName: "release/1.2"}, description: "release/1.2"},
		{name: "description falls back to branch", spec: pipelineSpec{branch: "main"}, description: "main"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := CombineChecks(nil, []Pipeline{tt.spec.build()})[0]
			if got.Key != tt.key || got.UpdatedOn != tt.updated || got.Description != tt.description {
				t.Errorf("check = %+v, want key=%q updated=%q description=%q", got, tt.key, tt.updated, tt.description)
			}
		})
	}
}
