package jira

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

type searchFilters struct {
	issueType, priority, label, reporter, text string
	unassigned                                 bool
}

var meRe = regexp.MustCompile(`(?i)^(me|@me|current(user)?)$`)

func escapeJQL(s string) string { return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) }

func (f searchFilters) empty() bool {
	return f.issueType == "" && f.priority == "" && f.label == "" && f.reporter == "" && f.text == "" && !f.unassigned
}
func buildSearchJQL(project, status, assignee, raw string, f searchFilters) string {
	if raw != "" {
		return raw
	}
	clauses := []string{}
	for _, v := range []struct{ name, value string }{
		{"project", project}, {"status", status}, {"assignee", assignee},
		{"issuetype", f.issueType}, {"priority", f.priority}, {"labels", f.label}, {"reporter", f.reporter},
	} {
		if v.value == "" {
			continue
		}
		if (v.name == "reporter" || v.name == "assignee") && meRe.MatchString(v.value) {
			clauses = append(clauses, v.name+" = currentUser()")
		} else {
			clauses = append(clauses, fmt.Sprintf(`%s = "%s"`, v.name, escapeJQL(v.value)))
		}
	}
	if f.text != "" {
		clauses = append(clauses, fmt.Sprintf(`text ~ "%s"`, escapeJQL(f.text)))
	}
	if f.unassigned {
		clauses = append(clauses, "assignee IS EMPTY")
	}
	if len(clauses) == 0 {
		return "ORDER BY updated DESC"
	}
	return strings.Join(clauses, " AND ") + " ORDER BY updated DESC"
}
func issueFields(requested []string, asJSON, view bool) []string {
	if len(requested) == 0 {
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			out = append(out, v)
			seen[v] = true
		}
	}
	for _, v := range requested {
		add(v)
	}
	if !asJSON {
		for _, v := range DefaultIssueFields {
			add(v)
		}
		if view {
			for _, v := range []string{"reporter", "created", "description", "labels", "parent"} {
				add(v)
			}
		}
	}
	return out
}
func validateIdentifier(v, name string) error {
	if strings.TrimSpace(v) == "" || v == "." || v == ".." || strings.ContainsAny(v, "/?#\\") || strings.IndexFunc(v, unicode.IsSpace) >= 0 || strings.IndexFunc(v, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s must be a non-empty identifier without whitespace or URL separators", name)
	}
	return nil
}
