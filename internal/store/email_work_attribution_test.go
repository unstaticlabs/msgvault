package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEmailWorkAttributionUsesScopedConfirmedEnvelopes(t *testing.T) {
	assertions := assert.New(t)
	owners := map[int64][]string{1: {"owner-a@example.test"}, 2: {"owner-b@example.test"}}
	base := EmailWorkActivity{SourceID: 2, MessageID: 7, PositiveSent: true, Incoming: true, ExternalRecipient: true, ConfirmedSourceSender: true}
	envelopes := []EmailWorkEnvelope{{Kind: "from", Address: "OWNER-B@example.test"}, {Kind: "to", Address: "recipient@example.test"}}
	outbound := AttributeEmailWorkActivity(base, envelopes, owners, true)
	assertions.True(outbound.AttributionComplete)
	assertions.True(outbound.ConfirmedSourceSender)
	assertions.True(outbound.ExternalRecipient)
	assertions.False(outbound.Incoming)
	inbound := AttributeEmailWorkActivity(base, []EmailWorkEnvelope{{Kind: "from", Address: "sender@example.test"}, {Kind: "bcc", Address: "owner-a@example.test"}}, owners, true)
	assertions.True(inbound.Incoming)
	assertions.True(inbound.AttributionComplete)
	inbound = AttributeEmailWorkActivity(base, []EmailWorkEnvelope{{Kind: "from", Address: "sender@example.test"}, {Kind: "bcc", Address: "owner-a@example.test"}}, map[int64][]string{2: owners[2]}, true)
	assertions.False(inbound.Incoming, "excluded ownership cannot establish a received item")
	spoofed := AttributeEmailWorkActivity(base, []EmailWorkEnvelope{{Kind: "from", Address: "owner-a@example.test"}, {Kind: "to", Address: "recipient@example.test"}}, owners, true)
	assertions.False(spoofed.ConfirmedSourceSender, "another account's identity cannot prove this source sent the mail")
	internal := AttributeEmailWorkActivity(base, []EmailWorkEnvelope{{Kind: "from", Address: "owner-b@example.test"}, {Kind: "to", Address: "owner-a@example.test"}}, owners, true)
	assertions.False(internal.ExternalRecipient)
	outOfScope := AttributeEmailWorkActivity(base, []EmailWorkEnvelope{{Kind: "from", Address: "owner-b@example.test"}, {Kind: "to", Address: "owner-a@example.test"}}, map[int64][]string{2: owners[2]}, true)
	assertions.True(outOfScope.ExternalRecipient, "the classification is confined to the selected identity population")
}

func TestEmailWorkAttributionKeepsIncompleteEvidenceUnknown(t *testing.T) {
	base := EmailWorkActivity{SourceID: 1}
	owners := map[int64][]string{1: {"owner@example.test"}}
	for _, tc := range []struct {
		name      string
		envelopes []EmailWorkEnvelope
		owners    map[int64][]string
		complete  bool
	}{
		{"missing from", []EmailWorkEnvelope{{Kind: "to", Address: "owner@example.test"}}, owners, true},
		{"merged participant without envelope", []EmailWorkEnvelope{{Kind: "from", Address: ""}, {Kind: "to", Address: "owner@example.test"}}, owners, true},
		{"multiple senders", []EmailWorkEnvelope{{Kind: "from", Address: "one@example.test"}, {Kind: "from", Address: "two@example.test"}, {Kind: "to", Address: "owner@example.test"}}, owners, true},
		{"display name cannot impersonate address", []EmailWorkEnvelope{{Kind: "from", Address: "owner@example.test <other@example.test>"}, {Kind: "to", Address: "owner@example.test"}}, owners, true},
		{"missing recipient", []EmailWorkEnvelope{{Kind: "from", Address: "owner@example.test"}}, owners, true},
		{"missing confirmed source", []EmailWorkEnvelope{{Kind: "from", Address: "owner@example.test"}, {Kind: "to", Address: "other@example.test"}}, map[int64][]string{}, true},
		{"unavailable provider facts", []EmailWorkEnvelope{{Kind: "from", Address: "owner@example.test"}, {Kind: "to", Address: "other@example.test"}}, owners, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertions := assert.New(t)
			got := AttributeEmailWorkActivity(base, tc.envelopes, tc.owners, tc.complete)
			assertions.False(got.AttributionComplete)
		})
	}
}
