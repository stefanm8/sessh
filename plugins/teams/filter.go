package teams

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Filters are a chain of small predicates because the combination rules are
// precise and worth keeping honest: repeating a flag ORs within itself (two
// --sender means either sender), and different flags AND across each other.
// --grep is the deliberate exception — repeating it ANDs, because a caller
// adding a second pattern is narrowing, not widening.
//
// --only-messages is default ON. ThreadActivity/* and Event/* are membership
// churn and call notifications, not conversation; left in they outnumber real
// chat in some channels and every query has to filter them out again.

var systemTypeRe = regexp.MustCompile(`(?i)^(ThreadActivity|Event)/`)

func isSystemMessage(m *Message) bool {
	return systemTypeRe.MatchString(m.MessageType.String())
}

// filterSpec is the validated, flag-independent shape of a query. Building it
// is where every usage error is caught; applying it can no longer fail.
type filterSpec struct {
	convTest      func(string) bool
	senders       []string // lower-cased needles, OR
	sinceMS       int64    // inclusive lower bound
	hasSince      bool
	untilMS       int64 // exclusive upper bound
	hasUntil      bool
	types         map[string]struct{}
	grep          []*regexp.Regexp // AND
	captureSource string
	includeSystem bool
}

var captureSources = []string{"idb", "ws", "rest"}

// buildFilter turns parsed flags plus the conversation directory into a spec.
// Everything it rejects is an exit-2 usage error: a bad date or an unresolvable
// chat name is something the caller must fix, not an empty result to report.
func (f *queryFlags) buildFilter(cmd *cobra.Command, dir *Directory, now time.Time) (filterSpec, error) {
	var spec filterSpec

	if cmd.Flags().Changed("since") {
		ms, err := ParseWhen("--since", f.since, now)
		if err != nil {
			return spec, err
		}
		spec.sinceMS, spec.hasSince = ms, true
	}
	if cmd.Flags().Changed("until") {
		ms, err := ParseWhen("--until", f.until, now)
		if err != nil {
			return spec, err
		}
		spec.untilMS, spec.hasUntil = ms, true
	}

	test, err := resolveConversations(f.conversation, dir)
	if err != nil {
		return spec, err
	}
	spec.convTest = test

	if f.captureSource != "" {
		cs := strings.ToLower(f.captureSource)
		if !slices.Contains(captureSources, cs) {
			return spec, fmt.Errorf("invalid --capture-source %q. Expected: %s",
				f.captureSource, strings.Join(captureSources, ", "))
		}
		spec.captureSource = cs
	}

	for _, s := range f.sender {
		spec.senders = append(spec.senders, strings.ToLower(s))
	}
	if len(f.types) > 0 {
		spec.types = make(map[string]struct{}, len(f.types))
		for _, t := range f.types {
			spec.types[t] = struct{}{}
		}
	}
	spec.grep = compileGrep(f.grep)

	// Agent footgun guard: filtering explicitly on a SYSTEM messageType while
	// the default --only-messages is still dropping those types returns zero
	// rows and no explanation. An explicit --type wins over the default.
	spec.includeSystem = f.includeSystem ||
		(cmd.Flags().Changed("only-messages") && !f.onlyMessages)
	if !spec.includeSystem {
		for _, t := range f.types {
			if systemTypeRe.MatchString(t) {
				spec.includeSystem = true
				break
			}
		}
	}
	return spec, nil
}

// resolveConversations maps each --conversation term to an id matcher. Exact id
// wins outright and is never ambiguous; otherwise the sidecar's titles, channel
// and team names and member lists are searched, and anything that resolves to
// more (or less) than one conversation is a usage error listing the candidates
// — guessing on the caller's behalf would silently answer a different question.
func resolveConversations(terms []string, dir *Directory) (func(string) bool, error) {
	if len(terms) == 0 {
		return nil, nil
	}
	hasDirectory := dir != nil && dir.Present && len(dir.Map) > 0

	var matchers []func(string) bool
	for _, term := range terms {
		if !hasDirectory {
			// Without the sidecar an id (or a prefix of one) is the only handle
			// the caller has; there are no names to match against.
			matchers = append(matchers, idOrPrefixMatcher(term))
			continue
		}
		if _, ok := dir.Map[term]; ok {
			matchers = append(matchers, func(id string) bool { return id == term })
			continue
		}

		lower := strings.ToLower(term)
		var hits []*Conversation
		for _, cv := range dir.Map {
			if strings.HasPrefix(cv.ID, term) || strings.Contains(strings.ToLower(searchableText(cv)), lower) {
				hits = append(hits, cv)
			}
		}
		// Go map iteration is randomized, so order the candidates before we
		// either pick one or report them: the same query must not resolve
		// differently, or print a differently ordered list, between runs.
		slices.SortFunc(hits, func(a, b *Conversation) int { return strings.Compare(a.ID, b.ID) })

		switch {
		case len(hits) == 0:
			// A raw-looking id may name a conversation the sidecar has never
			// seen (its thread was never opened), so still allow prefix
			// matching against the message data itself.
			if strings.HasPrefix(term, "19:") {
				matchers = append(matchers, idOrPrefixMatcher(term))
				continue
			}
			return nil, fmt.Errorf("no conversation matches %q. Try `sessh teams conversations` to list them, "+
				"or pass a full conversationId", term)
		case len(hits) > 1:
			return nil, fmt.Errorf("ambiguous --conversation %q matches %d conversations:\n%s\n"+
				"Narrow the term or pass a full conversationId", term, len(hits), candidateList(hits))
		}
		id := hits[0].ID
		matchers = append(matchers, func(cid string) bool { return cid == id })
	}

	return func(conversationID string) bool {
		for _, m := range matchers {
			if m(conversationID) {
				return true
			}
		}
		return false
	}, nil
}

func idOrPrefixMatcher(term string) func(string) bool {
	return func(id string) bool { return id == term || strings.HasPrefix(id, term) }
}

func searchableText(cv *Conversation) string {
	parts := []string{cv.Title, cv.DisplayName, cv.ShortTitle, cv.LongTitle, cv.ChannelName, cv.TeamName}
	parts = append(parts, cv.Members...)
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), "\n")
}

func candidateList(hits []*Conversation) string {
	const max = 8
	var b strings.Builder
	for i, h := range hits {
		if i == max {
			b.WriteString("\n  …")
			break
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("  - " + h.ID)
		if name := h.Name(); name != "" {
			b.WriteString("  (" + name + ")")
		}
	}
	return b.String()
}

// compileGrep compiles --grep patterns case-insensitively. An invalid pattern
// falls back to a literal substring match rather than failing the query: a
// stray metachar in a search term (or a JS-only construct such as a lookahead,
// which RE2 rejects outright) should still find the obvious thing.
func compileGrep(patterns []string) []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, p := range patterns {
		re, err := regexp.Compile("(?i)" + p)
		if err != nil {
			re = regexp.MustCompile("(?i)" + regexp.QuoteMeta(p))
		}
		out = append(out, re)
	}
	return out
}

func buildPredicate(spec filterSpec) func(*Message) bool {
	return func(m *Message) bool {
		if !spec.includeSystem && isSystemMessage(m) {
			return false
		}
		if spec.convTest != nil && !spec.convTest(m.ConversationID) {
			return false
		}
		if len(spec.senders) > 0 {
			haystack := strings.ToLower(strings.Join([]string{m.Sender, m.AuthorMRI, authorLabel(m)}, "\n"))
			if !slices.ContainsFunc(spec.senders, func(n string) bool { return strings.Contains(haystack, n) }) {
				return false
			}
		}
		if spec.hasSince || spec.hasUntil {
			t, ok := MessageTimeMS(m)
			if !ok {
				return false // no time cannot satisfy a bound
			}
			if spec.hasSince && t < spec.sinceMS {
				return false
			}
			if spec.hasUntil && t >= spec.untilMS {
				return false
			}
		}
		if spec.types != nil {
			if _, ok := spec.types[m.MessageType.String()]; !ok {
				return false
			}
		}
		for _, re := range spec.grep {
			if !re.MatchString(m.Body) {
				return false
			}
		}
		if spec.captureSource != "" && m.Source != spec.captureSource {
			return false
		}
		return true
	}
}
