package api

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/identityindex"
	"go.kenn.io/msgvault/internal/query"
)

// ensureIdentityCacheFixtureDatasets keeps hand-built API fixtures compatible
// with the current cache contract by deriving the identity datasets from
// the fixture's raw Parquet tables with the production builder. The derived
// relationship_activity dataset is a live dependency of the people, domain,
// participant-grouping, and timeline read paths, so an empty stand-in would
// make those queries return no rows.
func ensureIdentityCacheFixtureDatasets(
	tb testing.TB,
	db *sql.DB,
	analyticsDir string,
) {
	tb.Helper()
	ensureEmailWorkCacheFixtureDatasets(tb, db, analyticsDir)
	personDisplayNamesDir := filepath.Join(analyticsDir, "person_display_names")
	require.NoError(tb, os.MkdirAll(personDisplayNamesDir, 0o755), "create person_display_names fixture directory")
	personDisplayNamesPath := filepath.ToSlash(filepath.Join(personDisplayNamesDir, "person_display_names.parquet"))
	_, err := db.Exec(fmt.Sprintf(
		"COPY (SELECT 0::BIGINT AS participant_id, 0::BIGINT AS person_id, ''::VARCHAR AS display_name WHERE false) TO '%s' (FORMAT PARQUET)",
		personDisplayNamesPath,
	))
	require.NoError(tb, err, "write empty person_display_names fixture dataset")
	_, err = identityindex.Build(context.Background(), db, identityindex.BuildOptions{
		Mode:           identityindex.ModeFull,
		StagedBaseRoot: analyticsDir,
		OutputRoot:     analyticsDir,
	})
	require.NoError(tb, err, "derive identity fixture datasets")
}

func ensureEmailWorkCacheFixtureDatasets(t testing.TB, db *sql.DB, analyticsDir string) {
	t.Helper()
	for _, spec := range []struct{ name, selectSQL string }{
		{query.DatasetEmailWorkActions, "SELECT ''::VARCHAR AS action_uid,''::VARCHAR AS kind,0::BIGINT AS revision,false AS active,''::VARCHAR AS actor_kind,0::BIGINT AS actor_user_id,NULL::TIMESTAMP AS created_at,''::VARCHAR AS target_uid,0::BIGINT AS target_count WHERE false"},
		{query.DatasetEmailWorkFacts, "SELECT 0::BIGINT AS message_id,1::INTEGER AS extraction_version,''::VARCHAR AS outcome,''::VARCHAR AS facts WHERE false"},
		{query.DatasetEmailWorkSeeds, "SELECT 0::BIGINT AS ordinal,''::VARCHAR AS uid,0::BIGINT AS anchor_message_id,NULL::BIGINT AS live_message_id WHERE false"},
		{query.DatasetEmailWorkIdentities, "SELECT 0::BIGINT AS source_id,''::VARCHAR AS address WHERE false"},
		{query.DatasetEmailWorkDelivery, "SELECT 0::BIGINT AS message_id,0::BIGINT AS source_id,NULL::VARCHAR AS provider_thread_id,false AS native_sent,false AS native_draft,false AS provider_complete WHERE false"},
	} {
		path := filepath.Join(analyticsDir, spec.name, spec.name+".parquet")
		if _, err := os.Stat(path); err == nil {
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		_, err := db.Exec(fmt.Sprintf("COPY (%s) TO '%s' (FORMAT PARQUET)", spec.selectSQL, strings.ReplaceAll(filepath.ToSlash(path), "'", "''")))
		require.NoError(t, err, "write typed empty work dataset")
	}
}
