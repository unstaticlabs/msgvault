package store_test

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
	"testing"
)

func TestUnifiedSchemaNormalizesDraftRolesWithoutLosingEvidence(t *testing.T) {
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("imap", "legacy-draft@example.test")
	require.NoError(t, err)
	ids, err := st.EnsureLabelsBatch(source.ID, map[string]store.LabelInfo{
		"old":    {Name: "[QA] legacy draft", Type: "system", SystemRole: "draft"},
		"native": {Name: "[QA] native draft", Type: "system", SystemRole: "drafts"},
		"sent":   {Name: "[QA] contradictory sent", Type: "system", SystemRole: "sent"},
	})
	require.NoError(t, err)
	// Recreate the exact legacy literal, even if native label ingestion normalizes it.
	_, err = st.DB().Exec(st.Rebind("UPDATE labels SET system_role='draft' WHERE id=?"), ids["old"])
	require.NoError(t, err)
	_, err = st.DB().Exec("DELETE FROM applied_migrations WHERE name='email_work_draft_role_alias_v1'")
	require.NoError(t, err)
	before, err := st.DerivedDataRevision()
	require.NoError(t, err)
	require.NoError(t, st.InitSchemaContext(t.Context()))
	for key, want := range map[string]string{"old": "drafts", "native": "drafts", "sent": "sent"} {
		var got string
		require.NoError(t, st.DB().QueryRow(st.Rebind("SELECT system_role FROM labels WHERE id=?"), ids[key]).Scan(&got))
		assert.Equal(t, want, got)
	}
	after, err := st.DerivedDataRevision()
	require.NoError(t, err)
	assert.Equal(t, before+1, after)
	require.NoError(t, st.InitSchemaContext(t.Context()))
	repeated, err := st.DerivedDataRevision()
	require.NoError(t, err)
	assert.Equal(t, after, repeated)
	version, err := st.SchemaVersionContext(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 7, version)
}
