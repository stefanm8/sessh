package teams

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// Ordering has to be a total order, not just a sort. messages.jsonl is read as
// a stream, so two rows sharing a sort key would otherwise keep *read* order,
// which is not guaranteed to repeat across runs once capture appends to the
// file. Every comparison therefore falls back to the id, and identical inputs
// produce byte-identical output.

var messageSortFields = []string{"time", "sender", "conversation", "type", "captured"}

// sortValue is one row's comparison key: a number, a string, or absent.
type sortValue struct {
	num   int64
	text  string
	isNum bool
	ok    bool
}

func numKey(n int64, ok bool) sortValue {
	if !ok {
		return sortValue{}
	}
	return sortValue{num: n, isNum: true, ok: true}
}

func textKey(s string) sortValue {
	if s == "" {
		return sortValue{}
	}
	return sortValue{text: s, ok: true}
}

// messageSortKey extracts the comparable key for a sort field. An empty string
// is indistinguishable from an absent field once the row is decoded, so both
// count as missing — which is also the more useful reading, since "missing"
// sorts last rather than heading an ascending list.
func messageSortKey(m *Message, field string) sortValue {
	switch field {
	case "captured":
		if t, ok := parseAnyTime(m.CapturedAt); ok {
			return numKey(t.UnixMilli(), true)
		}
		return sortValue{}
	case "sender":
		return textKey(m.Sender)
	case "conversation":
		return textKey(firstNonEmpty(m.ConversationTitle, m.ConversationID))
	case "type":
		return textKey(m.MessageType.String())
	default:
		return numKey(MessageTimeMS(m))
	}
}

func compareSortValues(a, b sortValue) int {
	if a.isNum && b.isNum {
		switch {
		case a.num < b.num:
			return -1
		case a.num > b.num:
			return 1
		}
		return 0
	}
	return strings.Compare(a.text, b.text)
}

// sortMessages returns a newly ordered slice. Missing keys always sort last,
// independent of direction, so a `--order desc` query is never headed by rows
// whose time is simply unknown; only present-vs-present comparisons flip.
func sortMessages(rows []*Message, field, order string) []*Message {
	dir := 1
	if order == "desc" {
		dir = -1
	}
	if !slices.Contains(messageSortFields, field) {
		field = "time"
	}

	type keyed struct {
		msg *Message
		key sortValue
	}
	decorated := make([]keyed, len(rows))
	for i, m := range rows {
		decorated[i] = keyed{msg: m, key: messageSortKey(m, field)}
	}
	slices.SortStableFunc(decorated, func(a, b keyed) int {
		switch {
		case a.key.ok && !b.key.ok:
			return -1
		case !a.key.ok && b.key.ok:
			return 1
		case a.key.ok:
			if c := compareSortValues(a.key, b.key); c != 0 {
				return c * dir
			}
		}
		return strings.Compare(a.msg.ID, b.msg.ID)
	})

	out := make([]*Message, len(decorated))
	for i, d := range decorated {
		out[i] = d.msg
	}
	return out
}

// ---------------------------------------------------------------------------
// Sorting & pagination flags
// ---------------------------------------------------------------------------

// pagePlan is the resolved --sort/--order/--limit/--offset window. A nil limit
// means unlimited; a concrete 0 means zero rows.
type pagePlan struct {
	sort   string
	order  string
	limit  *int
	offset int
}

// pageDefaults are the per-command defaults. limit 0 means "no limit", matching
// the documented `--limit 0` spelling.
type pageDefaults struct {
	sort       string
	order      string
	limit      int
	sortFields []string
}

// resolveSortPage applies the per-command defaults and the --head/--tail sugar.
// Sort keys are per-command because the aggregate commands sort by things a
// message does not have (`conversations --sort count`), and rejecting those
// against the message field list would be wrong.
func resolveSortPage(cmd *cobra.Command, f *queryFlags, d pageDefaults) (pagePlan, error) {
	var plan pagePlan

	plan.order = firstNonEmpty(f.order, d.order, "asc")
	if plan.order != "asc" && plan.order != "desc" {
		return plan, fmt.Errorf("invalid --order %q. Expected: asc or desc", f.order)
	}

	allowed := d.sortFields
	if allowed == nil {
		allowed = messageSortFields
	}
	plan.sort = firstNonEmpty(f.sort, d.sort, "time")
	if !slices.Contains(allowed, plan.sort) {
		return plan, fmt.Errorf("invalid --sort %q. Expected one of: %s", plan.sort, strings.Join(allowed, ", "))
	}

	if d.limit > 0 {
		plan.limit = &d.limit
	}

	// --head/--tail set direction and size together. Unlike --limit they keep a
	// literal 0, because `--tail 0` asking for nothing is coherent.
	switch {
	case cmd.Flags().Changed("tail"):
		n, ok := asIntOrAll(f.tail, false)
		if !ok {
			return plan, fmt.Errorf("--tail expects a non-negative integer (got %q)", f.tail)
		}
		plan.order, plan.limit = "desc", &n
	case cmd.Flags().Changed("head"):
		n, ok := asIntOrAll(f.head, false)
		if !ok {
			return plan, fmt.Errorf("--head expects a non-negative integer (got %q)", f.head)
		}
		plan.order, plan.limit = "asc", &n
	}

	if cmd.Flags().Changed("limit") {
		n, ok := asIntOrAll(f.limit, true)
		if !ok {
			return plan, fmt.Errorf("--limit expects a non-negative integer or \"all\" (got %q)", f.limit)
		}
		if n == 0 {
			plan.limit = nil // 0 and "all" both mean unlimited
		} else {
			plan.limit = &n
		}
	}

	if cmd.Flags().Changed("offset") {
		n, ok := asIntOrAll(f.offset, false)
		if !ok {
			return plan, fmt.Errorf("--offset expects a non-negative integer (got %q)", f.offset)
		}
		plan.offset = n
	}
	return plan, nil
}

// asIntOrAll parses a count flag, treating "all" as 0 when the flag accepts it.
func asIntOrAll(v string, allowAll bool) (int, bool) {
	if allowAll && strings.EqualFold(v, "all") {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || strings.HasPrefix(v, "+") {
		return 0, false
	}
	return n, true
}

func paginate[T any](rows []T, offset int, limit *int) []T {
	if offset >= len(rows) {
		return nil
	}
	out := rows[offset:]
	if limit != nil && *limit < len(out) {
		out = out[:*limit]
	}
	return out
}
