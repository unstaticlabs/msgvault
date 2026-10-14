package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"go.kenn.io/msgvault/internal/query"
)

func emailWorkSnapshotTables(snapshot *cacheSourceSnapshot) []cacheSnapshotTable {
	facts := "SELECT CAST(NULL AS INTEGER) AS message_id,CAST(NULL AS INTEGER) AS extraction_version,CAST(NULL AS TEXT) AS outcome,CAST(NULL AS TEXT) AS facts WHERE 0"
	if snapshot.hasEmailWorkFacts {
		facts = "SELECT message_id,extraction_version,outcome,facts FROM email_linkage_facts"
	}
	seeds := "SELECT CAST(NULL AS INTEGER) AS ordinal,CAST(NULL AS TEXT) AS uid,CAST(NULL AS INTEGER) AS anchor_message_id,CAST(NULL AS INTEGER) AS live_message_id WHERE 0"
	if snapshot.hasEmailWorkSeeds {
		seeds = "SELECT ordinal,uid,anchor_message_id,live_message_id FROM email_work_seeds"
	}
	return []cacheSnapshotTable{
		{"email_linkage_facts", facts, "types={'message_id':'BIGINT','extraction_version':'INTEGER','outcome':'VARCHAR','facts':'VARCHAR'}"},
		{"email_work_seeds", seeds, "types={'ordinal':'BIGINT','uid':'VARCHAR','anchor_message_id':'BIGINT','live_message_id':'BIGINT'}"},
	}
}

func exportEmailWorkDatasets(snapshot *cacheSourceSnapshot, runExport func(string, string) error, root string, maxID int64) error {
	eligible := fmt.Sprintf("EXISTS (SELECT 1 FROM sqlite_db.messages m WHERE m.id=f.message_id AND %s AND m.id<=%d)", exportableMessageWhere("m"), maxID)
	facts := "SELECT NULL::BIGINT AS message_id,NULL::INTEGER AS extraction_version,NULL::VARCHAR AS outcome,NULL::VARCHAR AS facts WHERE FALSE"
	if snapshot.hasEmailWorkFacts {
		facts = "SELECT message_id,extraction_version,outcome,facts FROM sqlite_db.email_linkage_facts f WHERE " + eligible
	}
	seeds := "SELECT NULL::BIGINT AS ordinal,NULL::VARCHAR AS uid,NULL::BIGINT AS anchor_message_id,NULL::BIGINT AS live_message_id WHERE FALSE"
	if snapshot.hasEmailWorkSeeds {
		seeds = fmt.Sprintf("SELECT ordinal,uid,anchor_message_id,live_message_id FROM sqlite_db.email_work_seeds WHERE live_message_id IS NULL OR live_message_id<=%d", maxID)
	}
	sent := "FALSE"
	draft := "FALSE"
	complete := "FALSE"
	if snapshot.hasProviderLabels {
		carries := func(role string) string {
			return "EXISTS (SELECT 1 FROM sqlite_db.message_labels ml JOIN sqlite_db.labels l ON l.id=ml.label_id WHERE ml.message_id=m.id AND l.source_id=m.source_id AND " + role + ")"
		}
		gmailSent := carries("l.source_label_id='SENT'")
		gmailDraft := carries("l.source_label_id='DRAFT'")
		sent = "(lower(s.source_type)='gmail' AND " + gmailSent + ")"
		draft = "(lower(s.source_type)='gmail' AND " + gmailDraft + ")"
		complete = "lower(s.source_type)='gmail'"
		if snapshot.hasLabelRole && snapshot.hasIMAPMembership {
			sentIMAP := `EXISTS (SELECT 1 FROM sqlite_db.imap_message_memberships mm JOIN sqlite_db.labels l ON l.source_id=mm.source_id AND l.source_label_id=mm.mailbox WHERE mm.source_id=m.source_id AND mm.message_id=m.id AND l.system_role='sent')`
			draftIMAP := carries("l.system_role='draft'")
			flagItems := `json_extract(TRY_CAST(mm.flags AS JSON),'$[*]')`
			valid := `json_type(TRY_CAST(mm.flags AS JSON))='ARRAY' AND list_has_all(['VARCHAR'],list_transform(` + flagItems + `,flag->COALESCE(json_type(flag),'NULL')))`
			hasDraft := `list_contains(list_transform(` + flagItems + `,flag->LOWER(json_extract_string(flag,'$'))),'\draft')`
			flagsDraft := `EXISTS (SELECT 1 FROM sqlite_db.imap_message_memberships mm WHERE mm.source_id=m.source_id AND mm.message_id=m.id AND (` + valid + `) AND (` + hasDraft + `))`
			known := `EXISTS (SELECT 1 FROM sqlite_db.imap_message_memberships mm WHERE mm.source_id=m.source_id AND mm.message_id=m.id) AND NOT EXISTS (SELECT 1 FROM sqlite_db.imap_message_memberships mm WHERE mm.source_id=m.source_id AND mm.message_id=m.id AND NOT COALESCE((` + valid + `),FALSE))`
			sent = "(" + sent + " OR (lower(s.source_type)='imap' AND " + sentIMAP + "))"
			draft = "(" + draft + " OR (lower(s.source_type)='imap' AND (" + draftIMAP + " OR " + flagsDraft + ")))"
			complete = "(" + complete + " OR (lower(s.source_type)='imap' AND (" + known + ")))"
		}
	}
	delivery := fmt.Sprintf(`SELECT m.id AS message_id,m.source_id,
 CASE WHEN lower(s.source_type)='gmail' THEN c.source_conversation_id ELSE NULL END AS provider_thread_id,
 %s AS native_sent,%s AS native_draft,%s AS provider_complete FROM sqlite_db.messages m
 JOIN sqlite_db.sources s ON s.id=m.source_id JOIN sqlite_db.conversations c ON c.id=m.conversation_id
 WHERE %s AND m.id<=%d AND COALESCE(NULLIF(lower(m.message_type),''),'email')='email'`, sent, draft, complete, exportableMessageWhere("m"), maxID)
	for _, dataset := range []struct{ name, selectSQL string }{
		{query.DatasetEmailWorkFacts, facts}, {query.DatasetEmailWorkSeeds, seeds},
		{query.DatasetEmailWorkIdentities, "SELECT source_id,address FROM sqlite_db.account_identities"},
		{query.DatasetEmailWorkDelivery, delivery},
	} {
		destination := strings.ReplaceAll(filepath.Join(root, dataset.name, dataset.name+".parquet"), "'", "''")
		if err := runExport(dataset.name, fmt.Sprintf("COPY (%s) TO '%s' (FORMAT PARQUET,COMPRESSION 'zstd')", dataset.selectSQL, destination)); err != nil {
			return fmt.Errorf("export %s: %w", dataset.name, err)
		}
	}
	return nil
}
