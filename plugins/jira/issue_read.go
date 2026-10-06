package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/session"
)

type Transition struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	To     Status `json:"to"`
	Fields map[string]struct {
		Required        bool   `json:"required"`
		HasDefaultValue bool   `json:"hasDefaultValue"`
		Name            string `json:"name"`
	} `json:"fields"`
}

func getTransitions(ctx context.Context, s session.Session, key string) ([]Transition, []json.RawMessage, error) {
	var envelope struct {
		Transitions []json.RawMessage `json:"transitions"`
	}
	if err := getJSON(ctx, s, "/rest/api/3/issue/"+url.PathEscape(key)+"/transitions?expand=transitions.fields", &envelope); err != nil {
		return nil, nil, err
	}
	if envelope.Transitions == nil {
		return nil, nil, fmt.Errorf("Jira response omitted the transitions array")
	}
	items := make([]Transition, 0, len(envelope.Transitions))
	for _, raw := range envelope.Transitions {
		var v Transition
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, nil, err
		}
		if v.ID == "" || v.Name == "" {
			return nil, nil, fmt.Errorf("Jira returned a transition without ID or name")
		}
		items = append(items, v)
	}
	return items, envelope.Transitions, nil
}

func issueReadCommands(j *Jira) []*cobra.Command {
	commands := []*cobra.Command{}
	for _, kind := range []string{"comments", "history"} {
		kind := kind
		var asJSON bool
		var limit int
		cmd := &cobra.Command{Use: kind + " <KEY>", Short: "List issue " + kind, Args: cobra.ExactArgs(1)}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			if err := validateIdentifier(args[0], "issue key"); err != nil {
				return err
			}
			if err := validateLimit(limit); err != nil {
				return err
			}
			return j.withSession(false, func(ctx context.Context, s session.Session, cmd *cobra.Command, args []string) error {
				suffix, arrayKey := "comment", "comments"
				if kind == "history" {
					suffix, arrayKey = "changelog", "values"
				}
				raws, err := pagedValues(ctx, s, "/rest/api/3/issue/"+url.PathEscape(args[0])+"/"+suffix, arrayKey, limit)
				if err != nil {
					return err
				}
				for _, raw := range raws {
					var record struct {
						ID string `json:"id"`
					}
					if err := json.Unmarshal(raw, &record); err != nil {
						return err
					}
					if record.ID == "" {
						return fmt.Errorf("Jira returned a %s record without ID", kind)
					}
				}
				if asJSON {
					return printJSON(cmd, raws)
				}
				if len(raws) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No "+kind+" found.")
					return nil
				}
				for _, raw := range raws {
					var v struct {
						ID      string `json:"id"`
						Author  User   `json:"author"`
						Created string `json:"created"`
						Body    any    `json:"body"`
						Items   []struct {
							Field      string  `json:"field"`
							FromString *string `json:"fromString"`
							ToString   *string `json:"toString"`
						} `json:"items"`
					}
					if err := json.Unmarshal(raw, &v); err != nil {
						return fmt.Errorf("decode %s: %w", kind, err)
					}
					fmt.Fprintln(cmd.OutOrStdout(), format.Bold(v.ID+"  "+v.Author.DisplayName)+format.Gray("  "+v.Created))
					if kind == "comments" {
						fmt.Fprintln(cmd.OutOrStdout(), text(v.Body))
					} else {
						for _, it := range v.Items {
							from, to := "(empty)", "(empty)"
							if it.FromString != nil {
								from = *it.FromString
							}
							if it.ToString != nil {
								to = *it.ToString
							}
							fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s → %s\n", it.Field, from, to)
						}
					}
					fmt.Fprintln(cmd.OutOrStdout())
				}
				return nil
			})(cmd, args)
		}
		cmd.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
		cmd.Flags().IntVar(&limit, "limit", defaultLimit, "maximum results (0 for all)")
		commands = append(commands, cmd)
	}
	var asJSON bool
	transitions := &cobra.Command{Use: "transitions <KEY>", Short: "List available transition IDs and required fields", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateIdentifier(args[0], "issue key"); err != nil {
			return err
		}
		return j.withSession(false, func(ctx context.Context, s session.Session, cmd *cobra.Command, args []string) error {
			items, raws, err := getTransitions(ctx, s, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd, raws)
			}
			if len(items) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No transitions available.")
				return nil
			}
			rows := [][]string{}
			for _, v := range items {
				required := []string{}
				for k, f := range v.Fields {
					if f.Required && !f.HasDefaultValue {
						required = append(required, k)
					}
				}
				sort.Strings(required)
				rows = append(rows, []string{v.ID, v.Name, v.To.Name, strings.Join(required, ", ")})
			}
			fmt.Fprintln(cmd.OutOrStdout(), format.Table(rows, []string{"id", "transition", "destination", "required fields"}))
			return nil
		})(cmd, args)
	}}
	transitions.Flags().BoolVar(&asJSON, "json", false, "output raw transition and field metadata")
	return append(commands, transitions)
}
