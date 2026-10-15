package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestEmailWorkRoleChangesInvalidateExistingCacheFacts(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("imap", "qa-role@example.test")
	requirements.NoError(err)
	labels := map[string]store.LabelInfo{"mailbox": {Name: "Localized mailbox", Type: "system", SystemRole: "sent"}}
	ids, err := st.EnsureLabelsBatch(source.ID, labels)
	requirements.NoError(err)
	before, err := st.DerivedDataRevision()
	requirements.NoError(err)
	again, err := st.EnsureLabelsBatch(source.ID, labels)
	requirements.NoError(err)
	assertions.Equal(ids, again)
	unchanged, err := st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(before, unchanged)
	labels["mailbox"] = store.LabelInfo{Name: "Localized mailbox", Type: "system", SystemRole: "draft"}
	changed, err := st.EnsureLabelsBatch(source.ID, labels)
	requirements.NoError(err)
	assertions.Equal(ids, changed)
	after, err := st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(before+1, after)
	labels["mailbox"] = store.LabelInfo{Name: "Renamed mailbox", Type: "system", SystemRole: ""}
	_, err = st.EnsureLabelsBatch(source.ID, labels)
	requirements.NoError(err)
	cleared, err := st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(after+1, cleared)
	_, err = st.EnsureLabelsBatch(source.ID, labels)
	requirements.NoError(err)
	repeated, err := st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(cleared, repeated)
}
