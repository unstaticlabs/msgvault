package store_test

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
	"testing"
)

func TestUnifiedSchemaNormalizesDraftRolesWithoutLosingEvidence(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("imap", "legacy-draft@example.test")
	requirements.NoError(err)
	ids, err := st.EnsureLabelsBatch(source.ID, map[string]store.LabelInfo{
		"old":    {Name: "[QA] legacy draft", Type: "system", SystemRole: "draft"},
		"native": {Name: "[QA] native draft", Type: "system", SystemRole: "drafts"},
		"sent":   {Name: "[QA] contradictory sent", Type: "system", SystemRole: "sent"},
	})
	requirements.NoError(err)
	// Recreate the exact legacy literal, even if native label ingestion normalizes it.
	_, err = st.DB().Exec(st.Rebind("UPDATE labels SET system_role='draft' WHERE id=?"), ids["old"])
	requirements.NoError(err)
	_, err = st.DB().Exec("DELETE FROM applied_migrations WHERE name='email_work_draft_role_alias_v1'")
	requirements.NoError(err)
	before, err := st.DerivedDataRevision()
	requirements.NoError(err)
	requirements.NoError(st.InitSchemaContext(t.Context()))
	for key, want := range map[string]string{"old": "drafts", "native": "drafts", "sent": "sent"} {
		var got string
		requirements.NoError(st.DB().QueryRow(st.Rebind("SELECT system_role FROM labels WHERE id=?"), ids[key]).Scan(&got))
		assertions.Equal(want, got)
	}
	after, err := st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(before+1, after)
	requirements.NoError(st.InitSchemaContext(t.Context()))
	repeated, err := st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(after, repeated)
	version, err := st.SchemaVersionContext(t.Context())
	requirements.NoError(err)
	assertions.Equal(7, version)
}
