package query

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExploreDomainPopulationKeepsMembershipAndPages(t *testing.T) {
	b := NewTestDataBuilder(t)
	source := b.AddSource("owner@example.com")
	alpha := b.AddParticipant("sender@alpha.example", "alpha.example", "Alpha")
	beta := b.AddParticipant("recipient@beta.example", "beta.example", "Beta")
	other := b.AddParticipant("person@other.example", "other.example", "Other")
	base := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	addEmail := func(sender int64, minute int) int64 {
		return b.AddMessage(MessageOpt{SourceID: source, SenderID: &sender, SentAt: base.Add(time.Duration(minute) * time.Minute)})
	}
	senderMatch := addEmail(alpha, 1)
	b.AddTo(senderMatch, beta, "Beta")
	recipientMatch := addEmail(other, 2)
	b.AddTo(recipientMatch, alpha, "Alpha")
	b.AddCc(recipientMatch, beta, "Beta")
	const rosterConversation = int64(900)
	rosterMatch := b.AddMessage(MessageOpt{
		SourceID: source, SenderID: &other, SentAt: base.Add(3 * time.Minute),
		MessageType: "imessage", ConversationType: "group_chat", ConversationID: rosterConversation,
	})
	b.AddConversationParticipant(rosterConversation, alpha)
	b.AddConversationParticipant(rosterConversation, beta)
	addEmail(alpha, 4)            // Matches only the primary group.
	betaOnly := addEmail(beta, 5) // Matches only the additional group.
	addEmail(other, 6)
	engine := b.BuildEngine()

	messageKey := func(id int64) string { return fmt.Sprintf("source:%d:message:msg%d", source, id) }
	rosterKey := fmt.Sprintf("source:%d:conversation:%d", source, rosterConversation)
	intersection := Context{Domains: []string{"ALPHA.EXAMPLE", "absent.example"}, AdditionalDomainGroups: [][]string{{"beta.example"}}}
	for _, tc := range []struct {
		name    string
		context Context
		page    PageSpec
		keys    []string
		count   int64
	}{
		{"intersection", intersection, PageSpec{Limit: 50}, []string{rosterKey, messageKey(recipientMatch), messageKey(senderMatch)}, 3},
		{"middle page", intersection, PageSpec{Limit: 1, Offset: 1}, []string{messageKey(recipientMatch)}, 3},
		{"beyond end", intersection, PageSpec{Limit: 1, Offset: 30}, []string{}, 3},
		{"additional group alone", Context{AdditionalDomainGroups: [][]string{{"beta.example"}}}, PageSpec{Limit: 50}, []string{messageKey(betaOnly), rosterKey, messageKey(recipientMatch), messageKey(senderMatch)}, 4},
		{"no matches", Context{Domains: []string{"absent.example"}}, PageSpec{Limit: 50}, []string{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			result, err := engine.Explore(t.Context(), ExploreRequest{Context: tc.context, Page: tc.page})
			require.NoError(err)
			keys := make([]string, 0, len(result.Rows))
			for _, row := range result.Rows {
				keys = append(keys, row.Key)
			}
			assert.Equal(tc.keys, keys)
			assert.Equal(tc.count, result.TotalCount)
			if tc.name == "intersection" {
				require.Len(result.Rows, 3)
				require.NotNil(result.Rows[0].AnchorMessageID)
				assert.Equal(rosterMatch, *result.Rows[0].AnchorMessageID)
				assert.ElementsMatch([]int64{alpha, beta, other}, result.Rows[0].ParticipantIDs)
			}
		})
	}
}
