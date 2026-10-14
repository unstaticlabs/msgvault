package store_test

import (
	"context"
	"database/sql"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestEmailWorkAliasesUnionSplitAndSelectedScope(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()
	source, err := st.GetOrCreateSource("gmail", "qa-alias@example.test")
	requirements.NoError(err)
	var seeds []store.EmailWorkSeed
	var ids []int64
	for _, key := range []string{"first", "second", "late"} {
		id, err := st.UpsertMessage(linkageMessage(t, st, source.ID, key))
		requirements.NoError(err)
		seed, err := st.EmailWorkSeedForMessage(ctx, id)
		requirements.NoError(err)
		ids = append(ids, id)
		seeds = append(seeds, *seed)
		repeated, err := st.UpsertMessage(linkageMessage(t, st, source.ID, key))
		requirements.NoError(err)
		assertions.Equal(id, repeated)
		again, err := st.EmailWorkSeedForMessage(ctx, id)
		requirements.NoError(err)
		assertions.Equal(seed, again)
	}
	// Union redirects each historic alias to the oldest eligible allocation.
	for _, seed := range seeds {
		uid, err := store.ResolveEmailWorkAlias(seed, [][]int64{ids}, seeds)
		requirements.NoError(err)
		assertions.Equal(seeds[0].UID, uid)
	}
	// Splitting reopens the second alias's own anchor, rather than leaving a
	// union-find parent pointing into the other component.
	split := [][]int64{{ids[0], ids[2]}, {ids[1]}}
	uid, err := store.ResolveEmailWorkAlias(seeds[1], split, seeds)
	requirements.NoError(err)
	assertions.Equal(seeds[1].UID, uid)
	uid, err = store.ResolveEmailWorkAlias(seeds[2], split, seeds)
	requirements.NoError(err)
	assertions.Equal(seeds[0].UID, uid)
	scoped := [][]int64{{ids[1], ids[2]}}
	uid, err = store.ResolveEmailWorkAlias(seeds[2], scoped, seeds)
	requirements.NoError(err)
	assertions.Equal(seeds[1].UID, uid)
	_, err = store.ResolveEmailWorkAlias(seeds[0], scoped, seeds)
	assertions.ErrorIs(err, store.ErrEmailWorkAliasOutOfScope)
}

func TestEmailWorkAliasTombstoneSurvivesPhysicalIDReuse(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()
	source, err := st.GetOrCreateSource("gmail", "qa-reuse@example.test")
	requirements.NoError(err)
	id, err := st.UpsertMessage(linkageMessage(t, st, source.ID, "original"))
	requirements.NoError(err)
	old, err := st.EmailWorkSeedForMessage(ctx, id)
	requirements.NoError(err)
	_, err = st.DB().Exec(st.Rebind(`DELETE FROM messages WHERE id = ?`), id)
	requirements.NoError(err)
	tombstone, err := st.EmailWorkSeedForAlias(ctx, old.UID)
	requirements.NoError(err)
	assertions.Equal(old.AnchorMessageID, tombstone.AnchorMessageID)
	assertions.False(tombstone.LiveMessageID.Valid)
	// Force real physical-ID reuse on both supported backends. The seed must be
	// allocated anew; a prior alias stays deleted without retaining mail content.
	message := linkageMessage(t, st, source.ID, "replacement")
	insertSQL := `INSERT INTO messages (id,source_id,conversation_id,source_message_id,message_type)`
	if st.IsPostgreSQL() {
		insertSQL += ` OVERRIDING SYSTEM VALUE`
	}
	insertSQL += ` VALUES (?,?,?,?,'email')`
	_, err = st.DB().Exec(st.Rebind(insertSQL), id, source.ID, message.ConversationID, message.SourceMessageID)
	requirements.NoError(err)
	replacement, err := st.UpsertMessage(message)
	requirements.NoError(err)
	assertions.Equal(id, replacement)
	fresh, err := st.EmailWorkSeedForMessage(ctx, replacement)
	requirements.NoError(err)
	assertions.NotEqual(old.UID, fresh.UID)
	assertions.Greater(fresh.Ordinal, old.Ordinal)
	_, err = store.ResolveEmailWorkAlias(*tombstone, [][]int64{{id}}, []store.EmailWorkSeed{*fresh})
	requirements.ErrorIs(err, store.ErrEmailWorkAliasGone)
	assertions.Equal(sql.NullInt64{Int64: id, Valid: true}, fresh.LiveMessageID)
}

// A pre-feature archive may contain zero as a physical ID. Native UID
// allocation/migration protects its live anchor, and reuse cannot redirect it.
func TestEmailWorkLegacyZeroIDMigrationAndLifecycle(t *testing.T) {
	for _, physicalID := range []int64{0, -7} {
		t.Run(strconv.FormatInt(physicalID, 10), func(t *testing.T) {
			requirements := require.New(t)
			assertions := assert.New(t)
			st := testutil.NewTestStore(t)
			ctx := context.Background()
			source, err := st.GetOrCreateSource("gmail", "qa-legacy-zero@example.test")
			requirements.NoError(err)
			original := linkageMessage(t, st, source.ID, "legacy-zero")
			insert := `INSERT INTO messages(id,source_id,conversation_id,source_message_id,message_type)`
			if st.IsPostgreSQL() {
				insert += ` OVERRIDING SYSTEM VALUE`
			}
			insert += ` VALUES (?,?,?,?,'email')`
			_, err = st.DB().Exec(st.Rebind(insert), physicalID, source.ID, original.ConversationID, original.SourceMessageID)
			requirements.NoError(err)
			id, err := st.UpsertMessage(original)
			requirements.NoError(err)
			assertions.Equal(physicalID, id)
			seed, err := st.EmailWorkSeedForMessage(ctx, id)
			requirements.NoError(err)
			requirements.NotNil(seed)
			assertions.Equal(physicalID, seed.AnchorMessageID)
			assertions.Equal(sql.NullInt64{Int64: physicalID, Valid: true}, seed.LiveMessageID)
			requirements.NoError(st.InitSchema())
			migrated, err := st.EmailWorkSeedForAlias(ctx, seed.UID)
			requirements.NoError(err)
			assertions.Equal(seed, migrated, "native schema migration preserves the live allocation")
			_, err = st.DB().Exec(st.Rebind(`UPDATE messages SET id=10000 WHERE id=?`), id)
			requirements.Error(err, "live physical identity mutation must remain rejected")
			unchanged, err := st.EmailWorkSeedForAlias(ctx, seed.UID)
			requirements.NoError(err)
			assertions.Equal(seed, unchanged)
			_, err = st.DB().Exec(st.Rebind(`DELETE FROM messages WHERE id=?`), id)
			requirements.NoError(err)
			tombstone, err := st.EmailWorkSeedForAlias(ctx, seed.UID)
			requirements.NoError(err)
			assertions.False(tombstone.LiveMessageID.Valid)
			assertions.Equal(seed.AnchorMessageID, tombstone.AnchorMessageID)
			replacement := linkageMessage(t, st, source.ID, "replacement-zero")
			_, err = st.DB().Exec(st.Rebind(insert), physicalID, source.ID, replacement.ConversationID, replacement.SourceMessageID)
			requirements.NoError(err)
			reused, err := st.UpsertMessage(replacement)
			requirements.NoError(err)
			assertions.Equal(id, reused)
			fresh, err := st.EmailWorkSeedForMessage(ctx, reused)
			requirements.NoError(err)
			assertions.NotEqual(seed.UID, fresh.UID)
			_, err = store.ResolveEmailWorkAlias(*tombstone, [][]int64{{id}}, []store.EmailWorkSeed{*fresh})
			requirements.ErrorIs(err, store.ErrEmailWorkAliasGone)
		})
	}
}
