package query

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExploreFastListingPreservesProviderMailboxState(t *testing.T) {
	t.Parallel()
	assertions, requirements := assert.New(t), require.New(t)
	engine, ids := buildFastPathEquivalenceEngine(t)
	// Supply provider observations on the synthetic cache. The native fast page
	// now enriches from messages after pagination; both states must survive that
	// join just as they do through the complete analytical population.
	_, err := engine.db.Exec(`CREATE TEMP TABLE provider_mailbox_fixture AS SELECT * FROM messages`)
	requirements.NoError(err)
	_, err = engine.db.Exec(fmt.Sprintf(`CREATE OR REPLACE VIEW messages AS SELECT * REPLACE (
  CASE WHEN id = %d THEN 'inbox' ELSE 'outside' END AS inbox_state,
  CASE WHEN id = %d THEN 'unread' ELSE 'read' END AS read_state
 ) FROM provider_mailbox_fixture`, ids.email1, ids.email1))
	requirements.NoError(err)
	fast, legacy := runExploreBothPaths(t, engine, ExploreRequest{Page: PageSpec{Limit: 50}})
	assertions.Equal(legacy.Rows, fast.Rows)
	found := false
	for _, row := range fast.Rows {
		if row.AnchorMessageID != nil && *row.AnchorMessageID == ids.email1 {
			found = true
			assertions.Equal("inbox", row.InboxState)
			assertions.Equal("unread", row.ReadState)
		}
		if row.Kind == "conversation" {
			assertions.Equal("unknown", row.InboxState)
			assertions.Equal("unknown", row.ReadState)
		}
	}
	assertions.True(found, "the first synthetic email must appear on the page")
}
