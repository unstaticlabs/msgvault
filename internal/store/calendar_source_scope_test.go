package store

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/sourcescope"
	"testing"
	"time"
)

func TestCalendarOccurrencesRespectSourceScope(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	st := newRFC822IDBackfillBackendStore(t)
	data, id := calendarFixture(t, st, "visible", `{"id":"visible","start":{"dateTime":"2027-03-02T09:00:00Z"},"end":{"dateTime":"2027-03-02T10:00:00Z"},"attendees":[{"email":"guest@example.test"}]}`)
	hidden, err := st.GetOrCreateSource("gcal", "hidden@example.test")
	requirements.NoError(err)
	data.Message.SourceID = hidden.ID
	data.Message.SourceMessageID = "hidden"
	data.Conversation.SourceConversationID = "hidden"
	data.RawMIME = []byte(`{"id":"hidden","start":{"dateTime":"2027-03-02T11:00:00Z"},"end":{"dateTime":"2027-03-02T12:00:00Z"}}`)
	_, err = st.PersistMessage(data)
	requirements.NoError(err)
	from := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	for _, ids := range [][]int64{{data.Message.SourceID}, {}} {
		page, err := st.ListCalendarOccurrencesContext(sourcescope.WithSourceIDs(t.Context(), ids), from, from.AddDate(0, 1, 0))
		requirements.NoError(err)
		if len(ids) == 0 {
			assertions.Empty(page.Occurrences)
		} else {
			requirements.Len(page.Occurrences, 1)
			assertions.NotEqual(id, page.Occurrences[0].MessageID)
			assertions.Equal(hidden.ID, page.Occurrences[0].SourceID)
		}
	}
}
