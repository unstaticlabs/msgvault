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
	actions := "SELECT CAST(NULL AS TEXT) AS uid,CAST(NULL AS TEXT) AS kind,CAST(NULL AS INTEGER) AS revision,CAST(NULL AS INTEGER) AS active,CAST(NULL AS TEXT) AS actor_kind,CAST(NULL AS INTEGER) AS actor_user_id,CAST(NULL AS TEXT) AS created_at WHERE 0"
	targets := "SELECT CAST(NULL AS TEXT) AS action_uid,CAST(NULL AS TEXT) AS seed_uid WHERE 0"
	if snapshot.hasEmailWorkActions {
		actions = "SELECT uid,kind,revision,active,actor_kind,actor_user_id,created_at FROM email_work_actions"
		targets = "SELECT action_uid,seed_uid FROM email_work_action_targets"
	}
	return []cacheSnapshotTable{
		{"email_linkage_facts", facts, "types={'message_id':'BIGINT','extraction_version':'INTEGER','outcome':'VARCHAR','facts':'VARCHAR'}", nil},
		{"email_work_actions", actions, "types={'uid':'VARCHAR','kind':'VARCHAR','revision':'BIGINT','active':'BOOLEAN','actor_kind':'VARCHAR','actor_user_id':'BIGINT','created_at':'TIMESTAMP'}", nil},
		{"email_work_action_targets", targets, "types={'action_uid':'VARCHAR','seed_uid':'VARCHAR'}", nil},
		{"email_work_seeds", seeds, "types={'ordinal':'BIGINT','uid':'VARCHAR','anchor_message_id':'BIGINT','live_message_id':'BIGINT'}", nil},
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
	actions := "SELECT NULL::VARCHAR AS action_uid,NULL::VARCHAR AS kind,NULL::BIGINT AS revision,false AS active,NULL::VARCHAR AS actor_kind,NULL::BIGINT AS actor_user_id,NULL::TIMESTAMP AS created_at,NULL::VARCHAR AS target_uid,NULL::BIGINT AS target_count WHERE FALSE"
	if snapshot.hasEmailWorkActions {
		actions = `SELECT a.uid AS action_uid,a.kind,a.revision,CAST(a.active AS BOOLEAN) AS active,a.actor_kind,a.actor_user_id,CAST(a.created_at AS TIMESTAMP) AS created_at,t.seed_uid AS target_uid,(SELECT COUNT(*) FROM sqlite_db.email_work_action_targets all_t WHERE all_t.action_uid=a.uid) AS target_count FROM sqlite_db.email_work_actions a JOIN sqlite_db.email_work_action_targets t ON t.action_uid=a.uid`
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
			draftIMAP := carries("l.system_role IN ('draft','drafts')")
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
 CASE WHEN lower(s.source_type)='gmail' THEN %s ELSE NULL END AS provider_thread_id,
 %s AS native_sent,%s AS native_draft,%s AS provider_complete FROM sqlite_db.messages m
 JOIN sqlite_db.sources s ON s.id=m.source_id JOIN sqlite_db.conversations c ON c.id=m.conversation_id
 WHERE %s AND m.id<=%d AND COALESCE(NULLIF(lower(m.message_type),''),'email')='email'`, snapshot.identityExportSQL("c.source_conversation_id"), sent, draft, complete, exportableMessageWhere("m"), maxID)
	for _, dataset := range []struct{ name, selectSQL string }{
		{query.DatasetEmailWorkFacts, facts}, {query.DatasetEmailWorkSeeds, seeds},
		{query.DatasetEmailWorkIdentities, "SELECT source_id," + snapshot.identityExportSQL("address") + " AS address FROM sqlite_db.account_identities"},
		{query.DatasetEmailWorkDelivery, delivery},
		{query.DatasetEmailWorkActions, actions},
	} {
		destination := strings.ReplaceAll(filepath.Join(root, dataset.name, dataset.name+".parquet"), "'", "''")
		if err := runExport(dataset.name, fmt.Sprintf("COPY (%s) TO '%s' (FORMAT PARQUET,COMPRESSION 'zstd')", dataset.selectSQL, destination)); err != nil {
			return fmt.Errorf("export %s: %w", dataset.name, err)
		}
	}
	return nil
}
