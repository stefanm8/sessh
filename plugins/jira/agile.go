package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/session"
)

type Board struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Location struct {
		DisplayName string `json:"displayName"`
		ProjectKey  string `json:"projectKey"`
	} `json:"location"`
}

type Sprint struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	State         string `json:"state"`
	Goal          string `json:"goal"`
	StartDate     string `json:"startDate"`
	EndDate       string `json:"endDate"`
	CompleteDate  string `json:"completeDate"`
	OriginBoardID int64  `json:"originBoardId"`
}

func agileCommands(j *Jira) []*cobra.Command {
	board := &cobra.Command{Use: "board", Aliases: []string{"boards"}, Short: "Explore Jira Software boards"}
	var project, name, boardType string
	list := readListCommand(j, "list", "List visible boards", 0, []string{"ID", "name", "type", "project"},
		func(ctx context.Context, s session.Session, _ []string, limit int) ([]json.RawMessage, error) {
			q := url.Values{}
			if project != "" {
				q.Set("projectKeyOrId", project)
			}
			if name != "" {
				q.Set("name", name)
			}
			if boardType != "" {
				q.Set("type", boardType)
			}
			return pagedValues(ctx, s, "/rest/agile/1.0/board?"+q.Encode(), "values", limit)
		}, func(raw json.RawMessage) ([]string, error) {
			var b Board
			if err := decodeBoard(raw, &b); err != nil {
				return nil, err
			}
			return []string{strconv.FormatInt(b.ID, 10), b.Name, b.Type, b.Location.ProjectKey}, nil
		})
	list.PreRunE = func(*cobra.Command, []string) error {
		if boardType != "" && boardType != "scrum" && boardType != "kanban" {
			return fmt.Errorf("--type must be scrum or kanban")
		}
		return nil
	}
	list.Flags().StringVar(&project, "project", "", "project key or ID")
	list.Flags().StringVar(&name, "name", "", "board name search")
	list.Flags().StringVar(&boardType, "type", "", "scrum or kanban")
	board.AddCommand(list, agileViewCommand(j, "board"), agileIssuesCommand(j, "board"))

	sprint := &cobra.Command{Use: "sprint", Aliases: []string{"sprints"}, Short: "Explore Jira Software sprints"}
	var state string
	sprints := readListCommand(j, "list <board-id>", "List a Scrum board's sprints", 1, []string{"ID", "name", "state", "start", "end"},
		func(ctx context.Context, s session.Session, args []string, limit int) ([]json.RawMessage, error) {
			q := url.Values{}
			if state != "" {
				q.Set("state", state)
			}
			return pagedValues(ctx, s, "/rest/agile/1.0/board/"+args[0]+"/sprint?"+q.Encode(), "values", limit)
		}, func(raw json.RawMessage) ([]string, error) {
			var sp Sprint
			if err := decodeSprint(raw, &sp); err != nil {
				return nil, err
			}
			return []string{strconv.FormatInt(sp.ID, 10), sp.Name, sp.State, sp.StartDate, sp.EndDate}, nil
		})
	sprints.Args = agileIDArgs
	sprints.PreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Flags().Changed("state") && state == "" {
			return fmt.Errorf("--state must contain active, future, or closed")
		}
		if state == "" {
			return nil
		}
		seen := map[string]bool{}
		for _, part := range strings.Split(state, ",") {
			if part != "active" && part != "future" && part != "closed" {
				return fmt.Errorf("--state values must be active, future, or closed")
			}
			if seen[part] {
				return fmt.Errorf("--state repeats %q", part)
			}
			seen[part] = true
		}
		return nil
	}
	sprints.Flags().StringVar(&state, "state", "", "comma-separated active,future,closed (default all states)")
	sprint.AddCommand(sprints, agileViewCommand(j, "sprint"), agileIssuesCommand(j, "sprint"))
	return []*cobra.Command{board, sprint}
}

func agileIDArgs(cmd *cobra.Command, args []string) error {
	if err := cobra.ExactArgs(1)(cmd, args); err != nil {
		return err
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id <= 0 || strings.Trim(args[0], "0123456789") != "" {
		return fmt.Errorf("ID must be a positive integer")
	}
	return nil
}

func agileViewCommand(j *Jira, kind string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "view <" + kind + "-id>", Aliases: []string{"show"}, Short: "Show one " + kind, Args: agileIDArgs}
	cmd.RunE = j.withSession(false, func(ctx context.Context, s session.Session, cmd *cobra.Command, args []string) error {
		raw, err := requestJSON(ctx, s, http.MethodGet, "/rest/agile/1.0/"+kind+"/"+args[0], nil)
		if err != nil {
			return err
		}
		var rows [][]string
		if kind == "board" {
			var b Board
			if err := decodeBoard(raw, &b); err != nil {
				return err
			}
			rows = [][]string{{"ID", strconv.FormatInt(b.ID, 10)}, {"Name", b.Name}, {"Type", b.Type}, {"Project", b.Location.ProjectKey}, {"Location", b.Location.DisplayName}}
		} else {
			var sp Sprint
			if err := decodeSprint(raw, &sp); err != nil {
				return err
			}
			rows = [][]string{{"ID", strconv.FormatInt(sp.ID, 10)}, {"Name", sp.Name}, {"State", sp.State}, {"Goal", sp.Goal}, {"Start", sp.StartDate}, {"End", sp.EndDate}, {"Completed", sp.CompleteDate}}
			if sp.OriginBoardID != 0 {
				rows = append(rows, []string{"Board", strconv.FormatInt(sp.OriginBoardID, 10)})
			}
		}
		if asJSON {
			return printJSON(cmd, raw)
		}
		fmt.Fprintln(cmd.OutOrStdout(), format.Table(rows, nil))
		return nil
	})
	cmd.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	return cmd
}

func agileIssuesCommand(j *Jira, kind string) *cobra.Command {
	cmd := readListCommand(j, "issues <"+kind+"-id>", "List issues in a "+kind, 1,
		[]string{"key", "summary", "status", "type", "assignee", "updated"},
		func(ctx context.Context, s session.Session, args []string, limit int) ([]json.RawMessage, error) {
			// The old Agile issue endpoints are deprecated. Enhanced Software
			// endpoints use token pagination and preserve board/sprint ordering.
			q := url.Values{"fields": {strings.Join(DefaultIssueFields, ",")}}
			return tokenValues(ctx, s, "/rest/software/1.0/"+kind+"/"+args[0]+"/issue?"+q.Encode(), limit)
		}, func(raw json.RawMessage) ([]string, error) {
			var issue Issue
			if err := json.Unmarshal(raw, &issue); err != nil {
				return nil, err
			}
			if issue.Key == "" {
				return nil, fmt.Errorf("Jira issue has no key")
			}
			who := "Unassigned"
			if issue.Fields.Assignee != nil && issue.Fields.Assignee.DisplayName != "" {
				who = issue.Fields.Assignee.DisplayName
			}
			return []string{issue.Key, format.Truncate(issue.Fields.Summary, 50), StatusColor(issue.Fields.Status), issue.Fields.IssueType.Name, who, format.RelativeTime(issue.Fields.Updated)}, nil
		})
	cmd.Args = agileIDArgs
	cmd.Long = "List issues visible to your account in this " + kind + ". Jira Software access is required. Jira may return an empty list when no issues are visible, including when board permissions restrict access."
	return cmd
}

func decodeBoard(raw json.RawMessage, b *Board) error {
	if err := json.Unmarshal(raw, b); err != nil {
		return err
	}
	if b.ID <= 0 || b.Name == "" {
		return fmt.Errorf("Jira board is missing its id or name")
	}
	return nil
}

func decodeSprint(raw json.RawMessage, sp *Sprint) error {
	if err := json.Unmarshal(raw, sp); err != nil {
		return err
	}
	if sp.ID <= 0 || sp.Name == "" {
		return fmt.Errorf("Jira sprint is missing its id or name")
	}
	return nil
}
