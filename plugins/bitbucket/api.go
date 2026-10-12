package bitbucket

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/stefanm8/sessh/session"
)

// Bitbucket Cloud REST API v2.0 helpers. Every function takes a live Session
// and returns plain data; formatting lives in the command layer.

// Repo is a repository, reduced to the fields the CLI prints.
type Repo struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	IsPrivate   bool   `json:"is_private"`
	UpdatedOn   string `json:"updated_on"`
	Language    string `json:"language"`
	Size        int64  `json:"size"`
	MainBranch  struct {
		Name string `json:"name"`
	} `json:"mainbranch"`
	Links Links `json:"links"`
}

type Links struct {
	HTML struct {
		Href string `json:"href"`
	} `json:"html"`
}

type Account struct {
	DisplayName string `json:"display_name"`
	Nickname    string `json:"nickname"`
}

type Branch struct {
	Branch struct {
		Name string `json:"name"`
	} `json:"branch"`
	Commit struct {
		Hash string `json:"hash"`
	} `json:"commit"`
}

// PullRequest is one PR.
type PullRequest struct {
	ID           int     `json:"id"`
	Title        string  `json:"title"`
	State        string  `json:"state"`
	Author       Account `json:"author"`
	Source       Branch  `json:"source"`
	Destination  Branch  `json:"destination"`
	CreatedOn    string  `json:"created_on"`
	UpdatedOn    string  `json:"updated_on"`
	CommentCount int     `json:"comment_count"`
	TaskCount    int     `json:"task_count"`
	Links        Links   `json:"links"`
	Summary      struct {
		Raw string `json:"raw"`
	} `json:"summary"`
	Reviewers []Account `json:"reviewers"`
}

// Status is a commit/build status — the source of truth for external CI
// (Azure Pipelines, Jenkins) as well as anything else reporting to Bitbucket.
type Status struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	State       string `json:"state"`
	Description string `json:"description"`
	URL         string `json:"url"`
	CreatedOn   string `json:"created_on"`
	UpdatedOn   string `json:"updated_on"`
	Commit      struct {
		Hash string `json:"hash"`
	} `json:"commit"`
}

// Pipeline is a native Bitbucket Pipelines run.
type Pipeline struct {
	UUID        string `json:"uuid"`
	BuildNumber *int   `json:"build_number"`
	State       struct {
		Name   string `json:"name"`
		Result struct {
			Name string `json:"name"`
		} `json:"result"`
	} `json:"state"`
	Target struct {
		RefName  string `json:"ref_name"`
		Selector struct {
			Pattern string `json:"pattern"`
		} `json:"selector"`
		Commit struct {
			Hash string `json:"hash"`
		} `json:"commit"`
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
	} `json:"target"`
	CreatedOn   string `json:"created_on"`
	CompletedOn string `json:"completed_on"`
	Links       Links  `json:"links"`
}

// page is Bitbucket's envelope: { values, next, page, pagelen, size }.
type page struct {
	Values json.RawMessage `json:"values"`
	Next   string          `json:"next"`
}

// List is a page of results: the decoded values plus the raw JSON they came
// from, so `--json` can stay byte-faithful to what Bitbucket returned rather
// than echoing back only the fields this CLI happens to model.
type List[T any] struct {
	Items []T
	Raw   []json.RawMessage
}

// paginate walks a listing.
//
// `next` is an absolute api.bitbucket.org URL, which is a different origin
// from the same-origin `!api` path we are allowed to read — so instead of
// following it we re-request the SAME path with an incremented `page` and stop
// when Bitbucket stops handing us a `next`.
func paginate[T any](ctx context.Context, s session.Session, base string, params url.Values, max int) (*List[T], error) {
	out := &List[T]{}
	if params.Get("pagelen") == "" {
		params.Set("pagelen", "100")
	}
	for p := 1; p <= 1000; p++ {
		params.Set("page", strconv.Itoa(p))
		var env page
		if err := s.GetJSON(ctx, base+"?"+params.Encode(), &env); err != nil {
			return nil, err
		}
		var items []json.RawMessage
		if len(env.Values) > 0 {
			if err := json.Unmarshal(env.Values, &items); err != nil {
				return nil, err
			}
		}
		for _, it := range items {
			var v T
			if err := json.Unmarshal(it, &v); err != nil {
				continue
			}
			out.Items = append(out.Items, v)
			out.Raw = append(out.Raw, it)
			if max > 0 && len(out.Items) >= max {
				return out, nil
			}
		}
		if env.Next == "" || len(items) == 0 {
			return out, nil
		}
	}
	return out, nil
}

// Fields we ask Bitbucket to include, keeping payloads small and predictable.
const repoFields = "values.name,values.slug,values.full_name,values.description,values.is_private," +
	"values.updated_on,values.mainbranch.name,values.language,values.size,values.links.html.href,next"

const prFields = "values.id,values.title,values.state,values.author.display_name,values.author.nickname," +
	"values.source.branch.name,values.destination.branch.name,values.created_on,values.updated_on," +
	"values.comment_count,values.task_count,values.links.html.href,next"

// ListRepos lists repositories in the workspace, most-recently-updated first.
func ListRepos(ctx context.Context, s session.Session, workspace string, max int) (*List[Repo], error) {
	params := url.Values{"sort": {"-updated_on"}, "fields": {repoFields}}
	return paginate[Repo](ctx, s, apiPath("/repositories/"+workspace), params, max)
}

// GetRepo fetches one repository.
func GetRepo(ctx context.Context, s session.Session, workspace, repo string) (*Repo, json.RawMessage, error) {
	return get[Repo](ctx, s, apiPath("/repositories/"+workspace+"/"+repo), "repository "+workspace+"/"+repo)
}

func get[T any](ctx context.Context, s session.Session, path, what string) (*T, json.RawMessage, error) {
	raw, err := session.Get(ctx, s, path)
	if err != nil {
		return nil, nil, err
	}
	if !raw.OK {
		return nil, nil, apiError(raw, what)
	}
	var item T
	if err := raw.Decode(&item); err != nil {
		return nil, nil, err
	}
	return &item, raw.JSON, nil
}

// ListPullRequests lists PRs in the given states.
func ListPullRequests(ctx context.Context, s session.Session, workspace, repo string, states []string, max int) (*List[PullRequest], error) {
	params := url.Values{}
	for _, st := range states {
		params.Add("state", st)
	}
	params.Set("sort", "-updated_on")
	params.Set("fields", prFields)
	// The pullrequests endpoint caps pagelen at 50 (unlike repositories' 100)
	// and 400s on anything larger.
	params.Set("pagelen", "50")
	return paginate[PullRequest](ctx, s, apiPath("/repositories/"+workspace+"/"+repo+"/pullrequests"), params, max)
}

// GetPullRequest fetches one PR in full.
func GetPullRequest(ctx context.Context, s session.Session, workspace, repo string, id int) (*PullRequest, json.RawMessage, error) {
	path := fmt.Sprintf("%s/repositories/%s/%s/pullrequests/%d", apiPrefix, workspace, repo, id)
	return get[PullRequest](ctx, s, path, fmt.Sprintf("pull request #%d", id))
}

// apiError turns Bitbucket's error envelope into a readable one-liner.
func apiError(raw *session.Raw, what string) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if raw.JSON != nil && json.Unmarshal(raw.JSON, &e) == nil && e.Error.Message != "" {
		return fmt.Errorf("%s: %s", what, e.Error.Message)
	}
	return fmt.Errorf("%s: HTTP %d", what, raw.Status)
}

// ListPullRequestStatuses lists every build status attached to a PR.
func ListPullRequestStatuses(ctx context.Context, s session.Session, workspace, repo string, id, max int) (*List[Status], error) {
	params := url.Values{"sort": {"-updated_on"}}
	base := fmt.Sprintf("%s/repositories/%s/%s/pullrequests/%d/statuses", apiPrefix, workspace, repo, id)
	return paginate[Status](ctx, s, base, params, max)
}

// ListPipelinesForCommit lists native pipeline runs for one commit. Filtering
// by commit avoids scanning every run in the repository.
func ListPipelinesForCommit(ctx context.Context, s session.Session, workspace, repo, commit string, max int) (*List[Pipeline], error) {
	if commit == "" {
		return &List[Pipeline]{}, nil
	}
	params := url.Values{"target.commit.hash": {commit}, "sort": {"-created_on"}}
	return paginate[Pipeline](ctx, s, apiPath("/repositories/"+workspace+"/"+repo+"/pipelines"), params, max)
}

// Checks bundles both kinds of CI data attached to a PR's source commit.
type Checks struct {
	PullRequest    *PullRequest    `json:"-"`
	PullRequestRaw json.RawMessage `json:"pull_request"`
	Commit         string          `json:"commit"`
	Statuses       []Status        `json:"statuses"`
	Pipelines      []Pipeline      `json:"pipelines"`
}

// GetPullRequestChecks resolves a PR and gathers its CI.
func GetPullRequestChecks(ctx context.Context, s session.Session, workspace, repo string, id, max int) (*Checks, error) {
	pr, raw, err := GetPullRequest(ctx, s, workspace, repo, id)
	if err != nil {
		return nil, err
	}
	commit := pr.Source.Commit.Hash
	statuses, err := ListPullRequestStatuses(ctx, s, workspace, repo, id, max)
	if err != nil {
		return nil, err
	}
	pipelines, err := ListPipelinesForCommit(ctx, s, workspace, repo, commit, max)
	if err != nil {
		return nil, err
	}
	return &Checks{
		PullRequest: pr, PullRequestRaw: raw, Commit: commit,
		Statuses: statuses.Items, Pipelines: pipelines.Items,
	}, nil
}
