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
	tests := []struct {
		name string
		spec pipelineSpec
		want string
	}{
		{
			name: "a completed run reports its result, not COMPLETED",
			spec: pipelineSpec{stateName: "COMPLETED", resultName: "SUCCESSFUL"},
			want: "SUCCESSFUL",
		},
		{
			name: "a completed failure reports FAILED",
			spec: pipelineSpec{stateName: "COMPLETED", resultName: "FAILED"},
			want: "FAILED",
		},
		{
			name: "a running pipeline reports its state",
			spec: pipelineSpec{stateName: "IN_PROGRESS"},
			want: "IN_PROGRESS",
		},
		{
			name: "a completed run with no result keeps COMPLETED",
			spec: pipelineSpec{stateName: "COMPLETED"},
			want: "COMPLETED",
		},
		{
			name: "lower-case state and result are normalized",
			spec: pipelineSpec{stateName: "completed", resultName: "successful"},
			want: "SUCCESSFUL",
		},
		{
			name: "a missing state is UNKNOWN rather than empty",
			spec: pipelineSpec{},
			want: "UNKNOWN",
		},
		{
			name: "a result on a non-completed state is ignored",
			spec: pipelineSpec{stateName: "PENDING", resultName: "SUCCESSFUL"},
			want: "PENDING",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PipelineState(tt.spec.build()); got != tt.want {
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

func TestCheckCounts(t *testing.T) {
	tests := []struct {
		name                string
		states              []string
		ok, pending, failed int
	}{
		{"no checks counts nothing", nil, 0, 0, 0},
		{"all green", []string{"SUCCESSFUL", "SUCCESS"}, 2, 0, 0},
		{"a mixed board", []string{"SUCCESSFUL", "IN_PROGRESS", "FAILED", "STOPPED"}, 1, 1, 2},
		{"pending only", []string{"PENDING", "RUNNING"}, 0, 2, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checks := make([]Check, 0, len(tt.states))
			for _, s := range tt.states {
				checks = append(checks, Check{State: s})
			}
			ok, pending, failed := CheckCounts(checks)
			if ok != tt.ok || pending != tt.pending || failed != tt.failed {
				t.Errorf("CheckCounts(%v) = (%d, %d, %d), want (%d, %d, %d)",
					tt.states, ok, pending, failed, tt.ok, tt.pending, tt.failed)
			}
		})
	}
}

func TestChecksExitCode(t *testing.T) {
	tests := []struct {
		name   string
		states []string
		want   int
	}{
		{"no checks reported is a failure, not a pass", nil, 1},
		{"every check passed", []string{"SUCCESSFUL"}, 0},
		{"several checks passed", []string{"SUCCESSFUL", "SUCCESS", "PASSED"}, 0},
		{"still running is the dedicated pending code", []string{"IN_PROGRESS"}, 8},
		{"a failure outranks a pending check", []string{"IN_PROGRESS", "FAILED"}, 1},
		{"a failure outranks a success", []string{"SUCCESSFUL", "FAILED"}, 1},
		{"a pending check outranks a success", []string{"SUCCESSFUL", "PENDING"}, 8},
		{"an unknown state is treated as a failure", []string{"UNKNOWN"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checks := make([]Check, 0, len(tt.states))
			for _, s := range tt.states {
				checks = append(checks, Check{State: s})
			}
			if got := ChecksExitCode(checks); got != tt.want {
				t.Errorf("ChecksExitCode(%v) = %d, want %d", tt.states, got, tt.want)
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

	t.Run("a pipeline that already published a commit status is suppressed", func(t *testing.T) {
		got := CombineChecks(
			// Bitbucket writes the status URL with a trailing slash, the pipeline
			// link without one; they are the same run.
			[]Status{status("", "Pipeline", "SUCCESSFUL", "https://ci.example/run/1/", "", "", "")},
			[]Pipeline{pipelineSpec{
				buildNumber: intPtr(1), stateName: "COMPLETED", resultName: "SUCCESSFUL",
				href: "https://ci.example/run/1",
			}.build()},
		)
		if len(got) != 1 {
			t.Fatalf("CombineChecks() returned %d checks, want the duplicate dropped", len(got))
		}
		if got[0].Type != "status" {
			t.Errorf("survivor is %q, want the commit status to win", got[0].Type)
		}
	})

	t.Run("a pipeline with a different URL is kept", func(t *testing.T) {
		got := CombineChecks(
			[]Status{status("", "External", "SUCCESSFUL", "https://ci.example/run/1", "", "", "")},
			[]Pipeline{pipelineSpec{
				buildNumber: intPtr(2), stateName: "COMPLETED", resultName: "SUCCESSFUL",
				href: "https://ci.example/run/2",
			}.build()},
		)
		if len(got) != 2 {
			t.Fatalf("CombineChecks() returned %d checks, want 2", len(got))
		}
	})

	t.Run("URL-less runs are never deduped against each other", func(t *testing.T) {
		got := CombineChecks(
			[]Status{status("k", "", "SUCCESSFUL", "", "", "", "")},
			[]Pipeline{pipelineSpec{buildNumber: intPtr(3), stateName: "IN_PROGRESS"}.build()},
		)
		if len(got) != 2 {
			t.Fatalf("CombineChecks() returned %d checks, want both kept", len(got))
		}
	})

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

	t.Run("a pipeline key falls back from uuid to build number", func(t *testing.T) {
		got := CombineChecks(nil, []Pipeline{pipelineSpec{buildNumber: intPtr(42)}.build()})
		if got[0].Key != "42" {
			t.Errorf("Key = %q, want %q", got[0].Key, "42")
		}
		got = CombineChecks(nil, []Pipeline{pipelineSpec{uuid: "{u}", buildNumber: intPtr(42)}.build()})
		if got[0].Key != "{u}" {
			t.Errorf("Key = %q, want the uuid", got[0].Key)
		}
	})

	t.Run("a still-running pipeline is timestamped by created_on", func(t *testing.T) {
		got := CombineChecks(nil, []Pipeline{pipelineSpec{
			stateName: "IN_PROGRESS", createdOn: "2026-01-01T00:00:00Z",
		}.build()})
		if got[0].UpdatedOn != "2026-01-01T00:00:00Z" {
			t.Errorf("UpdatedOn = %q, want the created_on fallback", got[0].UpdatedOn)
		}
	})

	t.Run("a finished pipeline is timestamped by completed_on", func(t *testing.T) {
		got := CombineChecks(nil, []Pipeline{pipelineSpec{
			stateName: "COMPLETED", resultName: "SUCCESSFUL",
			createdOn: "2026-01-01T00:00:00Z", completedOn: "2026-01-01T00:10:00Z",
		}.build()})
		if got[0].UpdatedOn != "2026-01-01T00:10:00Z" {
			t.Errorf("UpdatedOn = %q, want the completed_on value", got[0].UpdatedOn)
		}
	})

	t.Run("the pipeline description falls back from ref name to branch", func(t *testing.T) {
		got := CombineChecks(nil, []Pipeline{pipelineSpec{refName: "release/1.2"}.build()})
		if got[0].Description != "release/1.2" {
			t.Errorf("Description = %q, want the ref name", got[0].Description)
		}
		got = CombineChecks(nil, []Pipeline{pipelineSpec{branch: "main"}.build()})
		if got[0].Description != "main" {
			t.Errorf("Description = %q, want the branch name", got[0].Description)
		}
	})
}
