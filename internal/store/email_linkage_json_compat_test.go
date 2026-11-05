package store_test

import (
	"context"
	jsonv1 "encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/mime"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

// Read a real predecessor fact string, then re-extract through the current
// Store path. The legacy encoder is fixture provenance, not production policy.
func TestEmailLinkageJSONV2PreservesLegacyFactBytesAndWorkIdentity(t *testing.T) {
	for _, domain := range []string{"example.test", "[node\u2028line\u2029test]"} {
		t.Run(domain, func(t *testing.T) {
			assertions, requirements := assert.New(t), require.New(t)
			st := testutil.NewTestStore(t)
			source, err := st.GetOrCreateSource("gmail", "owner@example.test")
			requirements.NoError(err)
			raw := []byte("Message-ID: <incoming&copy@" + domain + ">\r\nIn-Reply-To: <thread&reply@" + domain + ">\r\n\r\nOriginal body")
			id, err := st.PersistMessage(&store.MessagePersistData{Message: linkageMessage(t, st, source.ID, "json-compat"), RawMIME: raw})
			requirements.NoError(err)
			legacy, err := jsonv1.Marshal(mime.ExtractEmailLinkage(raw))
			requirements.NoError(err)
			assertions.Contains(string(legacy), `\u0026`, "predecessor stored HTML-significant identity characters escaped")
			if domain != "example.test" {
				assertions.Contains(string(legacy), `\u2028`)
				assertions.Contains(string(legacy), `\u2029`)
			}
			_, err = st.DB().ExecContext(t.Context(), st.Rebind(`UPDATE email_linkage_facts SET facts=? WHERE message_id=?`), string(legacy), id)
			requirements.NoError(err)
			var beforeUID string
			requirements.NoError(st.DB().QueryRowContext(t.Context(), st.Rebind(`SELECT uid FROM email_work_seeds WHERE live_message_id=?`), id).Scan(&beforeUID))
			beforeRevision, err := st.DerivedDataRevision()
			requirements.NoError(err)
			// A changed body makes the actual extraction run again without changing its identity evidence.
			requirements.NoError(st.UpsertMessageRaw(id, []byte("Message-ID: <incoming&copy@"+domain+">\r\nIn-Reply-To: <thread&reply@"+domain+">\r\n\r\nRevised body")))
			afterRevision, err := st.DerivedDataRevision()
			requirements.NoError(err)
			assertions.Equal(beforeRevision, afterRevision, "serializer migration alone must not invalidate derived facts")
			var current, afterUID string
			requirements.NoError(st.DB().QueryRowContext(t.Context(), st.Rebind(`SELECT facts FROM email_linkage_facts WHERE message_id=?`), id).Scan(&current))
			requirements.NoError(st.DB().QueryRowContext(t.Context(), st.Rebind(`SELECT uid FROM email_work_seeds WHERE live_message_id=?`), id).Scan(&afterUID))
			assertions.Equal(string(legacy), current)
			assertions.Equal(beforeUID, afterUID)
			facts, found, err := st.EmailLinkageEvidence(context.Background(), id)
			requirements.NoError(err)
			requirements.True(found)
			assertions.Equal("incoming&copy@"+domain, facts.MessageID)
			assertions.Equal([]string{"thread&reply@" + domain}, facts.Replies)
		})
	}
}
