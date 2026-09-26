package teams

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/plugin"
)

// The query commands are the half of this plugin that never touches a browser:
// they stream messages.jsonl and exit. None of them calls plugin.WithSession, so
// reading history works while `sessh teams capture` holds the profile — and works
// with no profile at all.
//
// Exit codes are a contract an agent can branch on:
//   0 success (including an empty result) · 2 usage · 3 no data · 4 not found
// "nothing matched your filter" and "that id does not exist" are different
// answers and must not share a code.

// queryCommands takes the plugin only for symmetry with captureCommand: none of
// these commands needs a profile, which is the whole point of them.
func queryCommands(_ *Teams) []*cobra.Command {
	cmds := []*cobra.Command{
		messagesCommand(),
		searchCommand(),
		showCommand(),
		sendersCommand(),
		conversationsCommand(),
		statsCommand(),
		schemaCommand(),
	}
	// A misspelled flag is a usage error, and cobra would otherwise report it
	// through the generic error path as an internal failure (exit 1).
	for _, c := range cmds {
		c.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
			return fail(2, "%v\nRun `sessh teams %s --help` for valid flags.", err, c.Name())
		})
	}
	return cmds
}

// noArgs rejects stray positionals with the usage exit code, for the same
// reason.
func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return fail(2, "%s: unexpected argument %q.\nRun `sessh teams %s --help` for usage.",
			cmd.Name(), args[0], cmd.Name())
	}
	return nil
}

// fail prints a diagnostic and returns the error carrying its exit code. The
// root command silences cobra's own error printing, so this is the whole
// message the caller sees.
func fail(code int, msg string, args ...any) error {
	fmt.Fprintf(os.Stderr, msg+"\n", args...)
	return plugin.Exit(code)
}

// ---------------------------------------------------------------------------
// Shared flags
// ---------------------------------------------------------------------------

type queryFlags struct {
	source string

	conversation  []string
	sender        []string
	since, until  string
	types         []string
	grep          []string
	captureSource string
	includeSystem bool
	onlyMessages  bool

	sort, order string
	limit       string
	offset      string
	head, tail  string

	outputFormat string
	fields       string
	raw          bool
	pretty       bool
	count        bool
}

func (f *queryFlags) bindSource(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.source, "source", "",
		"path to messages.jsonl (overrides $TEAMS_MESSAGES_FILE and the default data dir)")
}

func (f *queryFlags) bindFilter(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringArrayVarP(&f.conversation, "conversation", "c", nil,
		"restrict to a conversation: full id, unambiguous id prefix, or a chat, meeting, team or channel name. Repeatable = OR")
	fl.StringArrayVarP(&f.sender, "sender", "s", nil,
		"filter by sender display name or stable MRI (case-insensitive substring). Repeatable = OR")
	fl.StringVar(&f.since, "since", "",
		"lower time bound (inclusive): ISO-8601, relative (30m/2h/7d/2w), today/yesterday/now, or epoch")
	fl.StringVar(&f.until, "until", "", "upper time bound (exclusive). Same formats as --since")
	fl.StringArrayVarP(&f.types, "type", "t", nil,
		"filter by messageType (e.g. RichText/Html). Repeatable = OR. See: sessh teams schema")
	fl.StringArrayVarP(&f.grep, "grep", "g", nil,
		"case-insensitive regex/substring match against the body. Repeatable = AND")
	fl.StringVar(&f.captureSource, "capture-source", "", "filter by capture channel: idb, ws or rest")
	fl.BoolVar(&f.onlyMessages, "only-messages", true,
		"keep only human chat; drop ThreadActivity/* and Event/*")
	fl.BoolVar(&f.includeSystem, "include-system", false,
		"include system events (disables the default --only-messages)")
}

func (f *queryFlags) bindSortOrder(cmd *cobra.Command, sortKeys string) {
	cmd.Flags().StringVar(&f.sort, "sort", "", "sort key: "+sortKeys)
	cmd.Flags().StringVarP(&f.order, "order", "o", "", "sort direction: asc or desc")
}

func (f *queryFlags) bindLimitOffset(cmd *cobra.Command, limitHelp string) {
	cmd.Flags().StringVarP(&f.limit, "limit", "n", "", limitHelp)
	cmd.Flags().StringVar(&f.offset, "offset", "", "skip N rows (pagination)")
}

func (f *queryFlags) bindHeadTail(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.head, "head", "", "sugar: oldest N (== --order asc --limit N)")
	cmd.Flags().StringVar(&f.tail, "tail", "", "sugar: newest N (== --order desc --limit N)")
}

func (f *queryFlags) bindFormat(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&f.outputFormat, "format", "f", "",
		"output format: json | jsonl | table | text | csv (default: table on a terminal, json when piped)")
}

func (f *queryFlags) bindFields(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.fields, "fields", "", "comma-separated field projection, e.g. time,sender,body")
}

func (f *queryFlags) bindRaw(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.raw, "raw", false, "include the original provider payload (json/jsonl only)")
}

func (f *queryFlags) bindPretty(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.pretty, "pretty", false, "pretty-print JSON (default JSON is compact)")
}

func (f *queryFlags) bindCount(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.count, "count", false, "print only the number of matching rows")
}

// bindMessageOutput is the full output block shared by every row-producing
// command.
func (f *queryFlags) bindMessageOutput(cmd *cobra.Command) {
	f.bindFormat(cmd)
	f.bindFields(cmd)
	f.bindRaw(cmd)
	f.bindCount(cmd)
	f.bindPretty(cmd)
}

func (f *queryFlags) renderOpts(outFormat string) renderOpts {
	return renderOpts{format: outFormat, fields: parseFields(f.fields), raw: f.raw, pretty: f.pretty}
}

func anyChanged(cmd *cobra.Command, names ...string) bool {
	return slices.ContainsFunc(names, cmd.Flags().Changed)
}

// ---------------------------------------------------------------------------
// Loading
// ---------------------------------------------------------------------------

type loadedRows struct {
	rows      []*Message
	directory *Directory
	path      string
}

// load resolves the data file, builds the predicate and streams the survivors.
// It is where exit 3 (no data) and exit 2 (bad filter) are decided, so that
// every command reports them identically.
func (f *queryFlags) load(cmd *cobra.Command) (*loadedRows, error) {
	path, err := ResolveMessagesPath(f.source)
	if err != nil {
		return nil, fail(3, "%v", err)
	}

	// The sidecar is tiny and it is what turns opaque GUIDs into chat and
	// channel names, so it is always loaded; a missing one degrades to id-only.
	dir := LoadConversations(path)

	spec, err := f.buildFilter(cmd, dir, time.Now())
	if err != nil {
		return nil, fail(2, "%v", err)
	}

	verbose := plugin.GlobalFlags().Verbose
	if verbose {
		fmt.Fprintf(os.Stderr, "[source] %s\n", path)
		if dir.Present {
			fmt.Fprintf(os.Stderr, "[directory] %s\n", dir.Path)
		} else {
			fmt.Fprintln(os.Stderr, "[directory] (none — id-only mode)")
		}
	}

	shown := 0
	var rows []*Message
	predicate := buildPredicate(spec)
	scan, err := StreamMessages(path, func(m *Message) {
		dir.Enrich(m)
		if predicate(m) {
			rows = append(rows, m)
		}
	}, func(lineNo int) {
		if verbose && shown < 5 {
			fmt.Fprintf(os.Stderr, "[skip] malformed JSON on line %d\n", lineNo)
			shown++
		}
	})
	if err != nil {
		return nil, fmt.Errorf("reading captured messages: %w", err)
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "[read] %d current messages (%d physical records), %d malformed line(s) skipped, %d matched\n",
			scan.Total, scan.Physical, scan.Bad, len(rows))
	}
	return &loadedRows{rows: rows, directory: dir, path: path}, nil
}

// ---------------------------------------------------------------------------
// messages / search
// ---------------------------------------------------------------------------

const messageSortKeys = "time | sender | conversation | type | captured"

func messagesCommand() *cobra.Command {
	f := &queryFlags{}
	cmd := &cobra.Command{
		Use:     "messages",
		Aliases: []string{"msgs"},
		Short:   "Filter, sort and paginate captured messages",
		Long: `Filter, sort and paginate the captured Teams messages — the core query.

Filters combine with AND across different flags and with OR within a repeated
one, so ` + "`--sender A --sender B --since 24h`" + ` means "either person, in the last
day". --grep is the exception: repeating it ANDs, because a second pattern is
narrowing a search rather than widening it.

System noise (ThreadActivity/* and Event/*) is dropped by default; pass
--include-system to keep it, or filter for it explicitly with --type.`,
		Example: `  sessh teams messages --since 24h -s "Sam" -f json
  sessh teams messages -c 19:...@thread.v2 --tail 20
  sessh teams messages -c "General" --since today --count`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMessageQuery(cmd, f, nil)
		},
	}
	f.bindFilter(cmd)
	f.bindSortOrder(cmd, messageSortKeys)
	f.bindLimitOffset(cmd, `max rows; 0 or "all" for no limit (default 50)`)
	f.bindHeadTail(cmd)
	f.bindMessageOutput(cmd)
	f.bindSource(cmd)
	return cmd
}

func searchCommand() *cobra.Command {
	f := &queryFlags{}
	cmd := &cobra.Command{
		Use:   "search <text>",
		Short: "Full-text search across all conversations",
		Long: `Search message bodies across every captured conversation.

This is ` + "`messages --grep <text>`" + ` with the pattern as a positional argument:
the whole filter, sort and output surface of ` + "`messages`" + ` applies, and any
explicit --grep terms must ALSO match.`,
		Example: `  sessh teams search "deploy" --since 2026-01-01
  sessh teams search "ssh" -c "backend" -f json`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch len(args) {
			case 1:
			case 0:
				return fail(2, "search: missing <text> argument.\nUsage: sessh teams search <text> [flags]")
			default:
				return fail(2, "search: expected a single <text> argument (got %d). Quote the phrase.", len(args))
			}
			return runMessageQuery(cmd, f, args[:1])
		},
	}
	f.bindFilter(cmd)
	f.bindSortOrder(cmd, messageSortKeys)
	f.bindLimitOffset(cmd, `max rows; 0 or "all" for no limit (default 50)`)
	f.bindHeadTail(cmd)
	f.bindMessageOutput(cmd)
	f.bindSource(cmd)
	return cmd
}

func runMessageQuery(cmd *cobra.Command, f *queryFlags, extraGrep []string) error {
	// Output and paging are resolved before any I/O so a typo in -f or --sort
	// fails immediately instead of after streaming the whole file.
	outFormat, err := resolveFormat(f.outputFormat, format.IsTTY())
	if err != nil {
		return fail(2, "%v", err)
	}
	plan, err := resolveSortPage(cmd, f, pageDefaults{order: "desc", limit: 50})
	if err != nil {
		return fail(2, "%v", err)
	}
	f.grep = append(f.grep, extraGrep...)

	loaded, err := f.load(cmd)
	if err != nil {
		return err
	}
	if f.count {
		fmt.Println(len(loaded.rows))
		return nil
	}

	page := paginate(sortMessages(loaded.rows, plan.sort, plan.order), plan.offset, plan.limit)
	out, err := renderMessages(page, f.renderOpts(outFormat))
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

// ---------------------------------------------------------------------------
// show
// ---------------------------------------------------------------------------

func showCommand() *cobra.Command {
	f := &queryFlags{}
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Print one message by id",
		Long: `Print a single captured message.

Exits 4 when no message carries that id, so "the id is wrong" stays
distinguishable from "the filter matched nothing" (which is a success).

In json/jsonl this prints a single OBJECT rather than a one-element array.`,
		Example: `  sessh teams show 1700000000000
  sessh teams show 1700000000000 --raw --pretty`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fail(2, "show: missing <id> argument.\nUsage: sessh teams show <id> [--raw] [--pretty]")
			}
			id := args[0]

			outFormat, err := resolveFormat(f.outputFormat, format.IsTTY())
			if err != nil {
				return fail(2, "%v", err)
			}
			path, err := ResolveMessagesPath(f.source)
			if err != nil {
				return fail(3, "%v", err)
			}
			rows, _, err := Collect(path, func(m *Message) bool { return m.ID == id }, nil)
			if err != nil {
				return fmt.Errorf("reading captured messages: %w", err)
			}
			if len(rows) == 0 {
				return fail(4, "no message with id %q", id)
			}
			msg := LoadConversations(path).Enrich(rows[0])

			opts := f.renderOpts(outFormat)
			if outFormat == formatJSON || outFormat == formatJSONL {
				rec := decorateMessage(msg, f.raw)
				if opts.fields != nil {
					rec = projectMessage(msg, opts.fields)
				}
				b, err := jsonBytes(rec, f.pretty)
				if err != nil {
					return fmt.Errorf("rendering message %s: %w", id, err)
				}
				fmt.Println(string(b))
				return nil
			}
			out, err := renderMessages([]*Message{msg}, opts)
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
	}
	f.bindRaw(cmd)
	f.bindPretty(cmd)
	f.bindFormat(cmd)
	f.bindFields(cmd)
	f.bindSource(cmd)
	return cmd
}

// ---------------------------------------------------------------------------
// senders
// ---------------------------------------------------------------------------

func sendersCommand() *cobra.Command {
	f := &queryFlags{}
	cmd := &cobra.Command{
		Use:   "senders",
		Short: "List distinct senders with per-sender message counts",
		Long: `List who has been talking, most active first.

It shares the whole filter surface of ` + "`messages`" + `, so
` + "`senders -c backend --since 7d`" + ` answers "who was active in that channel
this week" rather than "who has ever posted".`,
		Example: `  sessh teams senders
  sessh teams senders -c "backend" -f json`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			outFormat, err := resolveFormat(f.outputFormat, format.IsTTY())
			if err != nil {
				return fail(2, "%v", err)
			}
			order := firstNonEmpty(f.order, "desc")
			if order != "asc" && order != "desc" {
				return fail(2, "invalid --order %q. Expected: asc or desc", f.order)
			}

			loaded, err := f.load(cmd)
			if err != nil {
				return err
			}

			type group struct {
				sender        string
				authorMRI     string
				authorStatus  string
				messages      int
				conversations map[string]struct{}
				first, last   int64
				seen          bool
			}
			groups := map[string]*group{}
			for _, m := range loaded.rows {
				key := authorGroupKey(m)
				name := authorLabel(m)
				g := groups[key]
				if g == nil {
					g = &group{sender: name, authorMRI: m.AuthorMRI,
						authorStatus: m.AuthorStatus, conversations: map[string]struct{}{}}
					groups[key] = g
				} else if m.Sender != "" {
					// A rename changes presentation, never the MRI grouping key.
					g.sender = m.Sender
					g.authorStatus = m.AuthorStatus
				}
				g.messages++
				if m.ConversationID != "" {
					g.conversations[m.ConversationID] = struct{}{}
				}
				if t, ok := MessageTimeMS(m); ok {
					if !g.seen || t < g.first {
						g.first = t
					}
					if !g.seen || t > g.last {
						g.last = t
					}
					g.seen = true
				}
			}

			if f.count {
				fmt.Println(len(groups))
				return nil
			}

			ordered := make([]*group, 0, len(groups))
			for _, g := range groups {
				ordered = append(ordered, g)
			}
			dir := 1
			if order == "desc" {
				dir = -1
			}
			// Ties break by name so the listing is reproducible.
			slices.SortFunc(ordered, func(a, b *group) int {
				if c := a.messages - b.messages; c != 0 {
					return c * dir
				}
				return strings.Compare(a.sender, b.sender)
			})
			if n, err := strconv.Atoi(f.limit); err == nil && n > 0 && n < len(ordered) {
				ordered = ordered[:n]
			}

			rows := make([]record, len(ordered))
			for i, g := range ordered {
				rows[i] = record{
					{"sender", g.sender},
					{"authorMRI", nullable(g.authorMRI)},
					{"authorStatus", g.authorStatus},
					{"messages", g.messages},
					{"conversations", len(g.conversations)},
					{"firstActivity", nullable(ToISO(g.first, g.seen))},
					{"lastActivity", nullable(ToISO(g.last, g.seen))},
				}
			}
			out, err := renderRows(rows, f.renderOpts(outFormat),
				[]string{"sender", "authorMRI", "messages", "conversations", "lastActivity"},
				map[string]int{"authorMRI": 42, "lastActivity": 20})
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
	}
	f.bindFilter(cmd)
	cmd.Flags().StringVarP(&f.order, "order", "o", "", "sort direction: asc or desc")
	cmd.Flags().StringVarP(&f.limit, "limit", "n", "", "max rows (default: all)")
	f.bindMessageOutput(cmd)
	f.bindSource(cmd)
	return cmd
}

// ---------------------------------------------------------------------------
// conversations
// ---------------------------------------------------------------------------

func conversationsCommand() *cobra.Command {
	f := &queryFlags{}
	cmd := &cobra.Command{
		Use:     "conversations",
		Aliases: []string{"convos"},
		Short:   "List conversations with counts, participants and last activity",
		Long: `Browse the captured chats, meetings, teams and channels.

Titles and participants come from the conversations.jsonl sidecar. When it is
absent this degrades to ids plus stats derived from the messages themselves
(title becomes "(unknown)") rather than failing — a query must never depend on
the sidecar existing.

With no filters this also lists conversations the sidecar knows about but whose
threads have never been opened, so it doubles as a discovery command.`,
		Example: `  sessh teams conversations
  sessh teams convos -f json`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			outFormat, err := resolveFormat(f.outputFormat, format.IsTTY())
			if err != nil {
				return fail(2, "%v", err)
			}
			plan, err := resolveSortPage(cmd, f, pageDefaults{
				sort: "last", order: "desc", sortFields: []string{"last", "count", "title"},
			})
			if err != nil {
				return fail(2, "%v", err)
			}

			loaded, err := f.load(cmd)
			if err != nil {
				return err
			}

			type group struct {
				id          string
				count       int
				first, last int64
				seen        bool
				senders     map[string]string
			}
			groups := map[string]*group{}
			at := func(id string) *group {
				g := groups[id]
				if g == nil {
					g = &group{id: id, senders: map[string]string{}}
					groups[id] = g
				}
				return g
			}
			for _, m := range loaded.rows {
				g := at(firstNonEmpty(m.ConversationID, "(unresolved conversation)"))
				g.count++
				g.senders[authorGroupKey(m)] = authorLabel(m)
				if t, ok := MessageTimeMS(m); ok {
					if !g.seen || t < g.first {
						g.first = t
					}
					if !g.seen || t > g.last {
						g.last = t
					}
					g.seen = true
				}
			}
			if !anyChanged(cmd, "conversation", "sender", "since", "until", "type", "grep", "capture-source") {
				for id := range loaded.directory.Map {
					at(id)
				}
			}

			type convRow struct {
				rec    record
				id     string
				title  string
				count  int
				lastMS int64
			}
			rows := make([]convRow, 0, len(groups))
			for _, g := range groups {
				meta := loaded.directory.Map[g.id]
				title := "(unknown)"
				var kind, channel, team, teamID string
				participantMap := map[string]string{}
				if meta != nil {
					if n := meta.Name(); n != "" {
						title = n
					}
					kind, channel, team, teamID = meta.KindOrType(), meta.ChannelName, meta.TeamName, meta.TeamID
					for i, name := range meta.Members {
						key := "name:" + strings.ToLower(name)
						if i < len(meta.MemberMRIs) && meta.MemberMRIs[i] != "" {
							key = "mri:" + strings.ToLower(meta.MemberMRIs[i])
						}
						participantMap[key] = name
					}
				}
				// A roster can be incomplete in Teams' cache. Always union it
				// with every stable author identity actually observed speaking.
				for key, name := range g.senders {
					participantMap[key] = name
				}
				participants := make([]string, 0, len(participantMap))
				for _, name := range participantMap {
					participants = append(participants, name)
				}
				slices.Sort(participants)
				// The sidecar's own lastMessageTimeUtc can be newer than
				// anything captured, for a thread that was listed but never
				// scrolled — take whichever is later.
				last, lastOK := g.last, g.seen
				if metaLast, ok := metadataTimeMS(metaLastTime(meta)); ok && (!lastOK || metaLast > last) {
					last, lastOK = metaLast, true
				}
				rows = append(rows, convRow{
					id: g.id, title: title, count: g.count, lastMS: last,
					rec: record{
						{"conversationId", g.id},
						{"title", title},
						{"type", nullable(metaType(meta))},
						{"kind", nullable(kind)},
						{"channelName", nullable(channel)},
						{"teamId", nullable(teamID)},
						{"teamName", nullable(team)},
						{"messages", g.count},
						{"senders", len(g.senders)},
						{"participants", participants},
						{"firstActivity", nullable(ToISO(g.first, g.seen))},
						{"lastActivity", nullable(ToISO(last, lastOK))},
					},
				})
			}

			if f.count {
				fmt.Println(len(rows))
				return nil
			}

			dir := 1
			if plan.order == "desc" {
				dir = -1
			}
			slices.SortFunc(rows, func(a, b convRow) int {
				var c int
				switch plan.sort {
				case "count":
					c = a.count - b.count
				case "title":
					c = compareTitles(a.title, b.title)
				default:
					c = cmp.Compare(a.lastMS, b.lastMS)
				}
				if c != 0 {
					return c * dir
				}
				return strings.Compare(a.id, b.id)
			})

			page := paginate(rows, plan.offset, plan.limit)
			recs := make([]record, len(page))
			for i, r := range page {
				recs[i] = r.rec
			}
			out, err := renderRows(recs, f.renderOpts(outFormat),
				[]string{"conversationId", "title", "kind", "messages", "senders", "lastActivity"},
				map[string]int{"conversationId": 40, "title": 30, "lastActivity": 20})
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
	}
	f.bindFilter(cmd)
	f.bindSortOrder(cmd, "last | count | title")
	f.bindLimitOffset(cmd, `max rows; 0 or "all" for no limit (default: all)`)
	f.bindMessageOutput(cmd)
	f.bindSource(cmd)
	return cmd
}

func metaLastTime(c *Conversation) any {
	if c == nil {
		return nil
	}
	return c.LastMessageTimeUTC
}

func metaType(c *Conversation) string {
	if c == nil {
		return ""
	}
	return c.Type
}

// compareTitles orders case-insensitively first, the way a human reading the
// list expects, and falls back to byte order so the result is still total.
func compareTitles(a, b string) int {
	if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// ---------------------------------------------------------------------------
// stats
// ---------------------------------------------------------------------------

func statsCommand() *cobra.Command {
	f := &queryFlags{}
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Aggregate counts over the current filter",
		Long: `Summarize the matching messages by conversation, sender, day and type.

It takes the same filters as ` + "`messages`" + `, so ` + "`stats --since today`" + ` is a
one-command answer to "what happened today, and where".`,
		Example: `  sessh teams stats --since today
  sessh teams stats -c "backend" -f json`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			outFormat, err := resolveFormat(f.outputFormat, format.IsTTY())
			if err != nil {
				return fail(2, "%v", err)
			}
			loaded, err := f.load(cmd)
			if err != nil {
				return err
			}
			if f.count {
				fmt.Println(len(loaded.rows))
				return nil
			}

			byType, byConv, byDay := counter{}, counter{}, counter{}
			bySender := authorCounter{}
			var first, last int64
			var seen bool
			for _, m := range loaded.rows {
				byType.bump(firstNonEmpty(m.MessageType.String(), "(none)"))
				bySender.bump(m)
				byConv.bump(firstNonEmpty(m.ConversationID, "(unresolved conversation)"))
				t, ok := MessageTimeMS(m)
				byDay.bump(DayKey(t, ok))
				if ok {
					if !seen || t < first {
						first = t
					}
					if !seen || t > last {
						last = t
					}
					seen = true
				}
			}

			convRows := make([]record, 0, len(byConv))
			for _, r := range byConv.ranked() {
				meta := loaded.directory.Map[r.key]
				title, kind, channel, team, teamID := "(unknown)", "", "", "", ""
				if meta != nil {
					if n := meta.Name(); n != "" {
						title = n
					}
					kind, channel, team, teamID = meta.KindOrType(), meta.ChannelName, meta.TeamName, meta.TeamID
				}
				convRows = append(convRows, record{
					{"conversationId", r.key},
					{"title", title},
					{"kind", nullable(kind)},
					{"channelName", nullable(channel)},
					{"teamId", nullable(teamID)},
					{"teamName", nullable(team)},
					{"count", r.count},
				})
			}

			result := record{
				{"total", len(loaded.rows)},
				{"range", record{
					{"first", nullable(ToISO(first, seen))},
					{"last", nullable(ToISO(last, seen))},
				}},
				{"byType", byType.ranked().records("type")},
				{"bySender", bySender.ranked().records()},
				{"byConversation", convRows},
				{"byDay", byDay.byKey().records("day")},
			}

			if outFormat == formatJSON || outFormat == formatJSONL {
				b, err := jsonBytes(result, f.pretty)
				if err != nil {
					return fmt.Errorf("rendering stats: %w", err)
				}
				fmt.Println(string(b))
				return nil
			}
			fmt.Println(renderStatsText(len(loaded.rows), first, last, seen,
				byType.ranked(), bySender.ranked().textTallies(), byConv.ranked(), byDay.byKey(), loaded.directory))
			return nil
		},
	}
	f.bindFilter(cmd)
	f.bindFormat(cmd)
	f.bindPretty(cmd)
	f.bindCount(cmd)
	f.bindSource(cmd)
	return cmd
}

type counter map[string]int

func (c counter) bump(key string) { c[key]++ }

type authorTally struct {
	key, sender, mri, status string
	count                    int
}

type authorCounter map[string]*authorTally
type authorTallies []*authorTally

func (c authorCounter) bump(m *Message) {
	key := authorGroupKey(m)
	r := c[key]
	if r == nil {
		r = &authorTally{key: key, sender: authorLabel(m), mri: m.AuthorMRI, status: m.AuthorStatus}
		c[key] = r
	} else if m.Sender != "" {
		r.sender, r.status = m.Sender, m.AuthorStatus
	}
	r.count++
}

func (c authorCounter) ranked() authorTallies {
	out := make(authorTallies, 0, len(c))
	for _, r := range c {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b *authorTally) int {
		if c := b.count - a.count; c != 0 {
			return c
		}
		if c := strings.Compare(a.sender, b.sender); c != 0 {
			return c
		}
		return strings.Compare(a.mri, b.mri)
	})
	return out
}

func (t authorTallies) records() []record {
	out := make([]record, len(t))
	for i, r := range t {
		out[i] = record{{"sender", r.sender}, {"authorMRI", nullable(r.mri)},
			{"authorStatus", r.status}, {"count", r.count}}
	}
	return out
}

func (t authorTallies) textTallies() tallies {
	out := make(tallies, len(t))
	for i, r := range t {
		label := r.sender
		if r.mri != "" {
			label += " [" + r.mri + "]"
		}
		out[i] = tally{key: label, count: r.count}
	}
	return out
}

type tally struct {
	key   string
	count int
}

type tallies []tally

func (c counter) tallies() tallies {
	out := make(tallies, 0, len(c))
	for k, n := range c {
		out = append(out, tally{k, n})
	}
	return out
}

// ranked orders by count desc, ties broken by key — a total order, so the
// "top 15" slices below never depend on map iteration order.
func (c counter) ranked() tallies {
	out := c.tallies()
	slices.SortFunc(out, func(a, b tally) int {
		if c := b.count - a.count; c != 0 {
			return c
		}
		return strings.Compare(a.key, b.key)
	})
	return out
}

// byKey orders by the key itself — what a per-day series needs.
func (c counter) byKey() tallies {
	out := c.tallies()
	slices.SortFunc(out, func(a, b tally) int { return strings.Compare(a.key, b.key) })
	return out
}

func (t tallies) records(keyName string) []record {
	out := make([]record, len(t))
	for i, r := range t {
		out[i] = record{{keyName, r.key}, {"count", r.count}}
	}
	return out
}

func (t tallies) top(n int) tallies {
	if len(t) > n {
		return t[:n]
	}
	return t
}

func renderStatsText(total int, first, last int64, seen bool,
	byType, bySender, byConv, byDay tallies, dir *Directory) string {
	section := func(title string, rows tallies, label func(tally) string) string {
		lines := []string{title}
		for _, r := range rows {
			lines = append(lines, fmt.Sprintf("  %d  %s", r.count, label(r)))
		}
		return strings.Join(lines, "\n")
	}
	self := func(r tally) string { return r.key }
	title := func(r tally) string {
		if meta := dir.Map[r.key]; meta != nil {
			if n := meta.Name(); n != "" {
				return n
			}
		}
		return "(unknown)"
	}
	rng := func(ms int64) string {
		if s := ToISO(ms, seen); s != "" {
			return s
		}
		return "(none)"
	}

	return strings.Join([]string{
		fmt.Sprintf("Total messages: %d", total),
		fmt.Sprintf("Range: %s .. %s", rng(first), rng(last)),
		"",
		section("By type:", byType, self),
		"",
		section("By sender (top 15):", bySender.top(15), self),
		"",
		section("By conversation (top 15):", byConv.top(15), title),
		"",
		section("By day:", byDay, self),
	}, "\n")
}

// ---------------------------------------------------------------------------
// schema
// ---------------------------------------------------------------------------

// schema exists so an agent can construct valid --type and --fields values
// without scraping human help output.

type schemaField struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Desc string `json:"desc"`
}

type schemaMessageType struct {
	Type string `json:"type"`
	Kind string `json:"kind"`
	Note string `json:"note"`
}

type messageSchema struct {
	Description          string              `json:"description"`
	Fields               []schemaField       `json:"fields"`
	KnownMessageTypes    []schemaMessageType `json:"knownMessageTypes"`
	SystemTypeRule       string              `json:"systemTypeRule"`
	DataSourceResolution []string            `json:"dataSourceResolution"`
}

var schemaFields = []schemaField{
	{"id", "string", "Stable dedupe key for the message."},
	{"time", "string (ISO-8601)", "DERIVED by this CLI from composeTime→capturedAt. Present in json/jsonl output; use this instead of parsing epoch-ms."},
	{"conversationId", "string", `Chat/channel id. Shapes: "19:<guid>_<guid>@unq.gbl.spaces" (chats) and "19:<hex>@thread.v2" (channels).`},
	{"conversationTitle", "string?", "Human-readable chat, meeting, team, or channel label from conversations.jsonl."},
	{"conversationType", "string?", "Normalized conversation kind: chat | meeting | channel | team | system."},
	{"channelName", "string?", "Channel/topic name captured from Teams threadProperties when applicable."},
	{"teamId", "string?", "Stable parent team/thread id for a channel when available."},
	{"teamName", "string?", "Parent team name for channel conversations when available."},
	{"sender", "string?", "Mutable display name. Use authorMRI for stable grouping/filtering."},
	{"authorMRI", "string?", "Stable Teams author identity (person, bot or app). A 19:... conversation creator is never treated as an author."},
	{"authorStatus", "string", "resolved | unresolved | system | display-name-only; distinguishes genuine system rows from unresolved people."},
	{"body", "string", "HTML-stripped plain text. Some system rows contain machine ids/gibberish."},
	{"contentType", "string", "Text | text | RichText/Html | application/user+xml."},
	{"messageType", "string", "See knownMessageTypes below; used by --type."},
	{"composeTime", "string", "Epoch MILLISECONDS as a string. May be absent → falls back to capturedAt."},
	{"capturedAt", "string (ISO-8601)", "When `sessh teams capture` recorded the message."},
	{"source", "string", "Capture channel: idb | ws | rest. Filter with --capture-source."},
	{"raw", "object", "Original unnormalized provider payload. Included only with --raw (may contain MRIs/ids)."},
}

var schemaMessageTypes = []schemaMessageType{
	{"RichText/Html", "message", "Most chat content."},
	{"Text", "message", "Plain-text chat messages."},
	{"RichText/Media_Card", "message", "Card/media messages."},
	{"ThreadActivity/AddMember", "system", "Dropped unless --include-system."},
	{"ThreadActivity/FileAdd", "system", "Dropped unless --include-system."},
	{"ThreadActivity/MemberJoined", "system", "Dropped unless --include-system."},
	{"Event/Call", "system", "Call activity. Dropped unless --include-system."},
}

func schemaObject() messageSchema {
	return messageSchema{
		Description:       "Normalized Teams message object produced and queried by `sessh teams`.",
		Fields:            schemaFields,
		KnownMessageTypes: schemaMessageTypes,
		SystemTypeRule:    `messageType matching /^(ThreadActivity|Event)\// is a system event and is dropped by the default --only-messages.`,
		DataSourceResolution: []string{
			"--source <path>", "$TEAMS_MESSAGES_FILE",
			filepath.Join(DataDirDefault(), "messages.jsonl"),
		},
	}
}

func schemaCommand() *cobra.Command {
	f := &queryFlags{}
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Print the message schema, field docs and known messageType values",
		Long: `Describe the message object: every field, what it means, and the messageType
values seen in practice — enough to build a valid --type or --fields without
reading the human help.`,
		Example: `  sessh teams schema
  sessh teams schema -f json | jq .`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			outFormat, err := resolveFormat(f.outputFormat, format.IsTTY())
			if err != nil {
				return fail(2, "%v", err)
			}
			obj := schemaObject()
			if outFormat == formatJSON || outFormat == formatJSONL {
				b, err := jsonBytes(obj, f.pretty)
				if err != nil {
					return fmt.Errorf("rendering schema: %w", err)
				}
				fmt.Println(string(b))
				return nil
			}

			lines := []string{"MESSAGE SCHEMA", obj.Description, "", "FIELDS"}
			for _, fd := range obj.Fields {
				lines = append(lines, fmt.Sprintf("  %s  (%s)", fd.Name, fd.Type), "      "+fd.Desc)
			}
			lines = append(lines, "", "KNOWN messageType VALUES (for --type)")
			for _, t := range obj.KnownMessageTypes {
				lines = append(lines, fmt.Sprintf("  %s  [%s]  %s", t.Type, t.Kind, t.Note))
			}
			lines = append(lines, "", obj.SystemTypeRule)
			fmt.Println(strings.Join(lines, "\n"))
			return nil
		},
	}
	f.bindFormat(cmd)
	f.bindPretty(cmd)
	return cmd
}
