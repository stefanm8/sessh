package bitbucket

import (
	"cmp"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/plugin"
	"github.com/stefanm8/sessh/session"
)

func prCommand(b *Bitbucket) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "pr <list|view|checks>",
		Aliases: []string{"prs"},
		Short:   "Work with pull requests and CI",
	}
	cmd.AddCommand(prListCommand(b), prViewCommand(b), prChecksCommand(b))
	return cmd
}

// resolveStates maps a --state flag to Bitbucket's state enum(s).
func resolveStates(flag string) []string {
	state := strings.TrimSpace(flag)
	switch strings.ToLower(state) {
	case "", "open":
		return []string{"OPEN"}
	case "all":
		return []string{"OPEN", "MERGED", "DECLINED", "SUPERSEDED"}
	case "closed":
		return []string{"MERGED", "DECLINED", "SUPERSEDED"}
	default:
		// Anything else is passed through as Bitbucket spells it. Trimmed,
		// because an untrimmed value reaches the API as `state="  MERGED  "`
		// and quietly matches nothing.
		return []string{strings.ToUpper(state)}
	}
}

func stateColor(state string) string {
	state = strings.ToUpper(state)
	switch state {
	case "OPEN":
		return format.Green(state)
	case "MERGED":
		return format.Magenta(state)
	case "DECLINED", "SUPERSEDED":
		return format.Red(state)
	default:
		return format.Gray(state)
	}
}

func branchOf(p PullRequest) string {
	src, dst := p.Source.Branch.Name, p.Destination.Branch.Name
	switch {
	case src != "" && dst != "":
		return src + " → " + dst
	case src != "":
		return src
	default:
		return dst
	}
}

func prListCommand(b *Bitbucket) *cobra.Command {
	var (
		asJSON bool
		limit  int
		state  string
		web    bool
	)
	cmd := &cobra.Command{
		Use:     "list <repo>",
		Aliases: []string{"ls"},
		Short:   "List pull requests",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, repo := Workspace(), args[0]
			if web {
				return format.OpenInBrowser(fmt.Sprintf("%s/%s/%s/pull-requests/", baseURL, ws, repo))
			}
			states := resolveStates(state)
			return plugin.WithSession(b, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				prs, err := ListPullRequests(ctx, s, ws, repo, states, limit)
				if err != nil {
					return err
				}
				if asJSON {
					return format.PrintJSON(prs.Raw)
				}
				if len(prs.Items) == 0 {
					fmt.Printf("No %s pull requests in %s/%s.\n", strings.ToLower(cmp.Or(state, "open")), ws, repo)
					return nil
				}
				rows := make([][]string, 0, len(prs.Items))
				for _, p := range prs.Items {
					rows = append(rows, []string{
						format.Green("#" + strconv.Itoa(p.ID)),
						format.Truncate(p.Title, 55),
						stateColor(p.State),
						format.Cyan(branchOf(p)),
						format.Gray(p.Author.DisplayName),
						format.Gray(format.RelativeTime(p.UpdatedOn)),
					})
				}
				fmt.Println(format.Table(rows, []string{"id", "title", "state", "branch", "author", "updated"}))
				fmt.Println(format.Gray(fmt.Sprintf("\n%s in %s/%s",
					format.Count(len(prs.Items), "pull request", "pull requests"), ws, repo)))
				return nil
			})(cmd, args)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	cmd.Flags().IntVar(&limit, "limit", 0, "cap the number of results")
	cmd.Flags().StringVar(&state, "state", "open", "open | merged | declined | closed | all")
	cmd.Flags().BoolVar(&web, "web", false, "open the page in your default browser instead")
	return cmd
}

func prViewCommand(b *Bitbucket) *cobra.Command {
	var (
		asJSON bool
		web    bool
	)
	cmd := &cobra.Command{
		Use:     "view <repo> <id>",
		Aliases: []string{"show"},
		Short:   "Show one pull request",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, repo := Workspace(), args[0]
			id, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("pull request id must be a number, got %q", args[1])
			}
			if web {
				return format.OpenInBrowser(fmt.Sprintf("%s/%s/%s/pull-requests/%d", baseURL, ws, repo, id))
			}
			return plugin.WithSession(b, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				p, raw, err := GetPullRequest(ctx, s, ws, repo, id)
				if err != nil {
					return err
				}
				if asJSON {
					return format.PrintJSON(raw)
				}
				fmt.Println(format.Bold(fmt.Sprintf("#%d %s", p.ID, p.Title)))
				fmt.Println(stateColor(p.State) + format.Gray(fmt.Sprintf(" • %s • %s", cmp.Or(p.Author.DisplayName, "unknown"), format.RelativeTime(p.CreatedOn))))
				fmt.Println(format.Gray(branchOf(*p)))
				fmt.Println()
				if body := strings.TrimSpace(p.Summary.Raw); body != "" {
					fmt.Println(body)
					fmt.Println()
				}
				format.Line("Comments", nonZero(p.CommentCount), 12)
				format.Line("Tasks", nonZero(p.TaskCount), 12)
				var reviewers []string
				for _, r := range p.Reviewers {
					if r.DisplayName != "" {
						reviewers = append(reviewers, r.DisplayName)
					}
				}
				format.Line("Reviewers", strings.Join(reviewers, ", "), 12)
				format.Line("URL", p.Links.HTML.Href, 12)
				return nil
			})(cmd, args)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	cmd.Flags().BoolVar(&web, "web", false, "open the page in your default browser instead")
	return cmd
}

func nonZero(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// Check is the one stable shape the two Bitbucket CI surfaces collapse into.
type Check struct {
	Type        string `json:"type"`
	Key         string `json:"key,omitempty"`
	Name        string `json:"name"`
	State       string `json:"state"`
	Description string `json:"description,omitempty"`
	Commit      string `json:"commit,omitempty"`
	URL         string `json:"url,omitempty"`
	CreatedOn   string `json:"created_on,omitempty"`
	UpdatedOn   string `json:"updated_on,omitempty"`
}

func prChecksCommand(b *Bitbucket) *cobra.Command {
	var (
		asJSON bool
		limit  int
		web    bool
	)
	cmd := &cobra.Command{
		Use:     "checks <repo> <id>",
		Aliases: []string{"check", "pipelines"},
		Short:   "Show CI checks for a pull request",
		Long: `Show the build statuses and pipeline runs attached to a pull request.

Exit codes follow gh, so this works as a CI gate:
  0  every reported check passed
  1  a check failed, or no checks were reported
  8  checks are still pending`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, repo := Workspace(), args[0]
			id, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("pull request id must be a number, got %q", args[1])
			}
			if web {
				return format.OpenInBrowser(fmt.Sprintf("%s/%s/%s/pull-requests/%d", baseURL, ws, repo, id))
			}
			return plugin.WithSession(b, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				result, err := GetPullRequestChecks(ctx, s, ws, repo, id, limit)
				if err != nil {
					return err
				}
				checks := CombineChecks(result.Statuses, result.Pipelines)
				if limit > 0 && len(checks) > limit {
					checks = checks[:limit]
				}
				code := ChecksExitCode(checks)

				if asJSON {
					if err := format.PrintJSON(struct {
						*Checks
						Combined []Check `json:"checks"`
					}{result, checks}); err != nil {
						return err
					}
					return plugin.Exit(code)
				}

				if len(checks) == 0 {
					fmt.Printf("No pipeline or build checks reported for PR #%d in %s/%s.\n", id, ws, repo)
					return plugin.Exit(code)
				}
				rows := make([][]string, 0, len(checks))
				for _, c := range checks {
					rows = append(rows, []string{
						format.Truncate(c.Name, 45),
						checkStateColor(c.State),
						format.Gray(c.Type),
						format.Gray(format.RelativeTime(cmp.Or(c.UpdatedOn, c.CreatedOn))),
						c.URL,
					})
				}
				fmt.Println(format.Table(rows, []string{"name", "state", "type", "updated", "url"}))
				ok, pending, failed := CheckCounts(checks)
				fmt.Println(format.Gray(fmt.Sprintf("\n%d successful, %d pending, %d failed", ok, pending, failed)))
				return plugin.Exit(code)
			})(cmd, args)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	cmd.Flags().IntVar(&limit, "limit", 0, "cap the number of checks")
	cmd.Flags().BoolVar(&web, "web", false, "open the page in your default browser instead")
	return cmd
}

// CombineChecks turns the two Bitbucket API shapes into one stable,
// script-friendly list. A native pipeline that already publishes a commit
// status is suppressed so it is not counted twice; both raw arrays stay in
// --json regardless.
func CombineChecks(statuses []Status, pipelines []Pipeline) []Check {
	out := make([]Check, 0, len(statuses)+len(pipelines))
	statusURLs := map[string]bool{}
	for _, st := range statuses {
		out = append(out, Check{
			Type: "status", Key: st.Key, Name: cmp.Or(st.Name, st.Key, "Build status"),
			State:       cmp.Or(strings.ToUpper(st.State), "UNKNOWN"),
			Description: st.Description, Commit: st.Commit.Hash, URL: st.URL,
			CreatedOn: st.CreatedOn, UpdatedOn: st.UpdatedOn,
		})
		if u := canonicalURL(st.URL); u != "" {
			statusURLs[u] = true
		}
	}
	for _, p := range pipelines {
		url := p.Links.HTML.Href
		if url != "" && statusURLs[canonicalURL(url)] {
			continue
		}
		key := p.UUID
		if key == "" && p.BuildNumber != nil {
			key = strconv.Itoa(*p.BuildNumber)
		}
		out = append(out, Check{
			Type: "pipeline", Key: key, Name: pipelineName(p), State: PipelineState(p),
			Description: cmp.Or(p.Target.RefName, p.Target.Branch.Name), Commit: p.Target.Commit.Hash, URL: url,
			CreatedOn: p.CreatedOn, UpdatedOn: cmp.Or(p.CompletedOn, p.CreatedOn),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return checkTime(out[i]) > checkTime(out[j])
	})
	return out
}

func checkTime(c Check) int64 {
	for _, s := range []string{c.UpdatedOn, c.CreatedOn} {
		if t, ok := format.ParseTime(s); ok {
			return t.UnixMilli()
		}
	}
	return 0
}

func canonicalURL(u string) string { return strings.TrimSuffix(u, "/") }

func pipelineName(p Pipeline) string {
	number := ""
	if p.BuildNumber != nil {
		number = fmt.Sprintf(" #%d", *p.BuildNumber)
	}
	return cmp.Or(p.Target.Selector.Pattern, "Pipeline") + number
}

// PipelineState flattens Bitbucket's nested state/result into one word.
func PipelineState(p Pipeline) string {
	name := cmp.Or(strings.ToUpper(p.State.Name), "UNKNOWN")
	if name == "COMPLETED" && p.State.Result.Name != "" {
		return strings.ToUpper(p.State.Result.Name)
	}
	return name
}

// CheckOutcome buckets a state into successful / pending / failed.
func CheckOutcome(state string) string {
	switch strings.ToUpper(state) {
	case "SUCCESSFUL", "SUCCESS", "PASSED":
		return "successful"
	case "INPROGRESS", "IN_PROGRESS", "PENDING", "RUNNING", "PAUSED", "NOT_RUN":
		return "pending"
	default:
		return "failed"
	}
}

// CheckCounts returns successful, pending and failed counts.
func CheckCounts(checks []Check) (ok, pending, failed int) {
	for _, c := range checks {
		switch CheckOutcome(c.State) {
		case "successful":
			ok++
		case "pending":
			pending++
		default:
			failed++
		}
	}
	return
}

// ChecksExitCode is gh's contract: 0 all passed, 1 failed or none, 8 pending.
func ChecksExitCode(checks []Check) int {
	if len(checks) == 0 {
		return 1
	}
	_, pending, failed := CheckCounts(checks)
	if failed > 0 {
		return 1
	}
	if pending > 0 {
		return 8
	}
	return 0
}

func checkStateColor(state string) string {
	switch CheckOutcome(state) {
	case "successful":
		return format.Green(state)
	case "pending":
		return format.Yellow(state)
	default:
		return format.Red(state)
	}
}
