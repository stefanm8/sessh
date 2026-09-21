package teams

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

// attributionBackfillReport makes the live-store migration auditable. Counts
// overlap by design: one record can recover both its conversation and author.
type attributionBackfillReport struct {
	SnapshotRecords int `json:"snapshotRecords"`
	RecordsChanged  int `json:"recordsChanged"`
	RecordsAppended int `json:"recordsAppended"`

	ConversationMissingBefore      int `json:"conversationMissingBefore"`
	ConversationFromOriginalThread int `json:"conversationFromOriginalThreadId"`
	ConversationFromLink           int `json:"conversationFromLink"`
	ConversationStillUnresolved    int `json:"conversationStillUnresolved"`
	GarbageSenderBefore            int `json:"garbageSenderBefore"`
	GarbageSenderResolved          int `json:"garbageSenderResolved"`
	GarbageSenderClearedUnresolved int `json:"garbageSenderClearedUnresolved"`
	MissingSenderBefore            int `json:"missingSenderBefore"`
	MissingSenderResolved          int `json:"missingSenderResolved"`
	UnresolvedUserAuthors          int `json:"unresolvedUserAuthors"`
	SystemAuthorless               int `json:"systemAuthorless"`
	AuthorMRIAdded                 int `json:"authorMRIAdded"`
	AuthorStatusAdded              int `json:"authorStatusAdded"`

	CurrentRecordsAfter       int    `json:"currentRecordsAfter"`
	PhysicalRecordsAfter      int    `json:"physicalRecordsAfter"`
	DuplicateContentKeysAfter int    `json:"duplicateContentKeysAfter"`
	MalformedLines            int    `json:"malformedLines"`
	MessagesBackup            string `json:"messagesBackup,omitempty"`
	ConversationsBackup       string `json:"conversationsBackup,omitempty"`
	DryRun                    bool   `json:"dryRun"`
}

func backfillCommand() *cobra.Command {
	var source string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "backfill-attribution",
		Short: "Append corrected channel/author attribution for existing messages",
		Long: `Repair attribution without rewriting the live JSONL file.

The command fixes a stable snapshot and appends one replacement record per
changed message. Query reads are last-record-wins by message id, making this
safe while capture is concurrently appending: no rename can strand the live
writer and no append can be lost. Before a real run, both stores are copied to
timestamped backup files. Rerunning is idempotent.`,
		Args: noArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			path, err := ResolveMessagesPath(source)
			if err != nil {
				return fail(3, "%v", err)
			}
			report, err := runAttributionBackfill(path, dryRun)
			if err != nil {
				return err
			}
			b, err := jsonBytes(report, true)
			if err != nil {
				return err
			}
			fmt.Println(string(b))
			return nil
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "messages.jsonl to repair (same resolution as query commands)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report changes without backups or appends")
	return cmd
}

func runAttributionBackfill(path string, dryRun bool) (*attributionBackfillReport, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	limit, err := completeJSONLSize(f)
	_ = f.Close()
	if err != nil {
		return nil, err
	}

	last := map[string]int{}
	report := &attributionBackfillReport{DryRun: dryRun}
	if err := scanMessagesTo(path, limit, func(m *Message, lineNo int) {
		if m.ID != "" {
			last[m.ID] = lineNo
		}
	}, func(int) { report.MalformedLines++ }); err != nil {
		return nil, err
	}

	dir := LoadConversations(path)
	resolver := newIdentityResolver()
	for mri, name := range dir.NamesByMRI {
		resolver.observe(mri, name)
	}
	// Resolve against all current records before changing any one of them, so a
	// name observed late in the file can repair an earlier MRI-only record.
	if err := scanMessagesTo(path, limit, func(m *Message, lineNo int) {
		if m.ID != "" && last[m.ID] != lineNo {
			return
		}
		raw := rawObject(m.Raw)
		normalizeMessageAttribution(m, raw, nil)
		resolver.observeRaw(raw)
		resolver.observe(m.AuthorMRI, m.Sender)
	}, nil); err != nil {
		return nil, err
	}

	if !dryRun {
		stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
		report.MessagesBackup, err = backupPrefix(path, limit, stamp)
		if err != nil {
			return nil, fmt.Errorf("backing up messages: %w", err)
		}
		convPath := ConversationsPathFor(path)
		if conv, openErr := os.Open(convPath); openErr == nil {
			convLimit, sizeErr := completeJSONLSize(conv)
			_ = conv.Close()
			if sizeErr != nil {
				return nil, fmt.Errorf("reading conversation backup boundary: %w", sizeErr)
			}
			report.ConversationsBackup, err = backupPrefix(convPath, convLimit, stamp)
			if err != nil {
				return nil, fmt.Errorf("backing up conversations: %w", err)
			}
		}
	}

	var out *os.File
	if !dryRun {
		out, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		defer out.Close()
	}

	var appendErr error
	err = scanMessagesTo(path, limit, func(m *Message, lineNo int) {
		if appendErr != nil {
			return
		}
		if m.ID != "" && last[m.ID] != lineNo {
			return
		}
		report.SnapshotRecords++
		before := *m
		raw := rawObject(m.Raw)
		if before.ConversationID == "" {
			report.ConversationMissingBefore++
		}
		garbage := before.Sender != "" && cleanDisplayName(before.Sender) == ""
		if garbage {
			report.GarbageSenderBefore++
		}
		if before.Sender == "" {
			report.MissingSenderBefore++
		}

		normalizeMessageAttribution(m, raw, resolver)
		if before.ConversationID == "" {
			_, source := conversationIDOf(raw)
			switch source {
			case "originalThreadId":
				report.ConversationFromOriginalThread++
			case "conversationLink":
				report.ConversationFromLink++
			}
			if m.ConversationID == "" {
				report.ConversationStillUnresolved++
			}
		}
		if garbage {
			if m.Sender != "" {
				report.GarbageSenderResolved++
			} else {
				report.GarbageSenderClearedUnresolved++
			}
		}
		if before.Sender == "" && m.Sender != "" {
			report.MissingSenderResolved++
		}
		if m.AuthorStatus == authorUnresolved && m.AuthorMRI != "" {
			report.UnresolvedUserAuthors++
		}
		if m.AuthorStatus == authorSystem {
			report.SystemAuthorless++
		}
		if before.AuthorMRI == "" && m.AuthorMRI != "" {
			report.AuthorMRIAdded++
		}
		if before.AuthorStatus == "" && m.AuthorStatus != "" {
			report.AuthorStatusAdded++
		}

		if samePersistedAttribution(&before, m) {
			return
		}
		report.RecordsChanged++
		if dryRun {
			return
		}
		line, marshalErr := marshalJSONL(m)
		if marshalErr != nil {
			appendErr = marshalErr
			return
		}
		n, writeErr := out.Write(line)
		if writeErr == nil && n != len(line) {
			writeErr = io.ErrShortWrite
		}
		if writeErr != nil {
			appendErr = writeErr
			return
		}
		report.RecordsAppended++
	}, nil)
	if err != nil {
		return nil, err
	}
	if appendErr != nil {
		return nil, appendErr
	}
	if out != nil {
		if err := out.Sync(); err != nil {
			return nil, err
		}
	}

	audit, err := auditCurrentMessages(path)
	if err != nil {
		return nil, err
	}
	report.CurrentRecordsAfter = audit.current
	report.PhysicalRecordsAfter = audit.physical
	report.DuplicateContentKeysAfter = audit.duplicateContent
	return report, nil
}

func rawObject(raw json.RawMessage) map[string]any {
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func samePersistedAttribution(a, b *Message) bool {
	return a.ConversationID == b.ConversationID && a.Sender == b.Sender &&
		a.AuthorMRI == b.AuthorMRI && a.AuthorStatus == b.AuthorStatus
}

func backupPrefix(path string, limit int64, stamp string) (string, error) {
	src, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer src.Close()
	dstPath := path + ".attribution-backup-" + stamp
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		_ = dst.Close()
		if !ok {
			_ = os.Remove(dstPath)
		}
	}()
	if _, err := io.CopyN(dst, src, limit); err != nil {
		return "", err
	}
	if err := dst.Sync(); err != nil {
		return "", err
	}
	ok = true
	abs, err := filepath.Abs(dstPath)
	if err != nil {
		return dstPath, nil
	}
	return abs, nil
}

type messageAudit struct{ current, physical, duplicateContent int }

func auditCurrentMessages(path string) (messageAudit, error) {
	seenContent := map[string]string{}
	var out messageAudit
	res, err := StreamMessages(path, func(m *Message) {
		if key := contentKey(m); key != "" {
			if prior, ok := seenContent[key]; ok && prior != m.ID {
				out.duplicateContent++
			} else {
				seenContent[key] = m.ID
			}
		}
	}, nil)
	out.current, out.physical = res.Total, res.Physical
	return out, err
}
