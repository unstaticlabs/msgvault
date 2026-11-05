package store_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/persontiers"
	"go.kenn.io/msgvault/internal/sourcescope"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestChatDiscoveryScopesBeforeRankingAndPageLimit(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	st := testutil.NewTestStore(t)
	hidden, err := st.GetOrCreateSource("beeper", "hidden@example.test")
	requirements.NoError(err)
	allowed, err := st.GetOrCreateSource("beeper", "allowed@example.test")
	requirements.NoError(err)
	seedDiscoveryChat(t, st, hidden.ID, "Jordan Exact", "direct_chat", time.Unix(200, 0))
	visible, _ := seedDiscoveryChat(t, st, allowed.ID, "Jordan Partial", "direct_chat", time.Unix(100, 0))
	for _, ids := range [][]int64{{allowed.ID}, {}} {
		page, err := st.SearchChatsContext(sourcescope.WithSourceIDs(t.Context(), ids), store.ChatDiscoveryQuery{Query: "Jordan Exact", Limit: 1})
		requirements.NoError(err)
		if len(ids) == 0 {
			assertions.Empty(page.Results)
		} else {
			requirements.Len(page.Results, 1)
			assertions.Equal(visible, page.Results[0].ConversationID)
		}
		assertions.False(page.HasMore, "hidden matching rows do not change pagination")
	}
	page, err := st.SearchChatsContext(sourcescope.WithSourceIDs(t.Context(), []int64{allowed.ID}), store.ChatDiscoveryQuery{Query: "Jordan", SourceID: hidden.ID, Limit: 1})
	requirements.NoError(err)
	assertions.Empty(page.Results, "a caller filter cannot replace its scope")
}

func TestMessagingRoutesScopeBeforePagination(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	st := testutil.NewTestStore(t)
	person, peer := messagingPerson(t, st, "scope-peer")
	hidden, _ := messagingConversation(t, st, "hidden-account", "hidden-chat", "direct_chat", peer)
	allowed, visible := messagingConversation(t, st, "visible-account", "visible-chat", "direct_chat", peer)
	for _, ids := range [][]int64{{allowed}, {}} {
		page, err := st.GetPersonMessagingRoutesContext(sourcescope.WithSourceIDs(t.Context(), ids), store.PersonMessagingRouteQuery{PersonUID: person.VCardUID, Limit: 1})
		requirements.NoError(err)
		if len(ids) == 0 {
			assertions.Empty(page.Routes.Items)
		} else {
			requirements.Len(page.Routes.Items, 1)
			assertions.Equal(visible, page.Routes.Items[0].ConversationID)
		}
		assertions.False(page.Routes.HasMore)
	}
	page, err := st.GetPersonMessagingRoutesContext(sourcescope.WithSourceIDs(t.Context(), []int64{allowed}), store.PersonMessagingRouteQuery{PersonUID: person.VCardUID, SourceID: hidden, Limit: 1})
	requirements.NoError(err)
	assertions.Empty(page.Routes.Items)
}

func TestPersonTierCountsOnlyAuthorizedSourceEvents(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	f, personID := newActivityQueryFixture(t)
	owner := f.EnsureParticipant("owner@example.com", "Owner", "example.com")
	var peer int64
	requirements.NoError(f.Store.DB().QueryRowContext(t.Context(), f.Store.Rebind(`SELECT participant_id FROM person_participants WHERE person_id=?`), personID).Scan(&peer))
	hidden, err := f.Store.GetOrCreateSource("gmail", "hidden@example.test")
	requirements.NoError(err)
	requirements.NoError(f.Store.AddAccountIdentity(hidden.ID, "owner@example.com", "manual"))
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	for _, source := range []int64{f.Source.ID, hidden.ID} {
		conversation, err := f.Store.EnsureConversation(source, "tier-scope-thread", "Fixture")
		requirements.NoError(err)
		message := &store.Message{SourceID: source, SourceMessageID: "tier-scope-message", ConversationID: conversation, MessageType: "email", SenderID: sql.NullInt64{Int64: peer, Valid: true}, SentAt: sql.NullTime{Time: at, Valid: true}}
		id, err := f.Store.UpsertMessage(message)
		requirements.NoError(err)
		requirements.NoError(f.Store.ReplaceMessageRecipients(id, "from", []int64{peer}, []string{"Peer"}))
		requirements.NoError(f.Store.ReplaceMessageRecipients(id, "to", []int64{owner}, []string{"Owner"}))
	}
	projectTierSignals(t, f, "UTC", 25)
	options := store.TierOptionsAsOf(store.PersonTierOptions{Config: persontiers.DefaultConfig()}, at)
	all, err := f.Store.ExplainPersonTierContext(t.Context(), personID, options)
	requirements.NoError(err)
	assertions.Equal(2, all.Signals.InboundLong)
	for _, ids := range [][]int64{{f.Source.ID}, {}} {
		explanation, err := f.Store.ExplainPersonTierContext(sourcescope.WithSourceIDs(t.Context(), ids), personID, options)
		requirements.NoError(err)
		assertions.Equal(len(ids), explanation.Signals.InboundLong)
	}
}
