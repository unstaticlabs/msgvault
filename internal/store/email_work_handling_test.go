package store_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

func workActivity(id, source int64, header string, incoming bool, second int) store.EmailWorkActivity {
	return store.EmailWorkActivity{EmailWorkMember: workMember(id, source, header), OccurredAt: time.Date(2026, 1, 1, 0, 0, second, 0, time.UTC), Incoming: incoming, AttributionComplete: true}
}

func completedReply(id int64, target string, second int) store.EmailWorkActivity {
	outgoing := workActivity(id, 2, fmt.Sprintf("Message-ID: <sent-%d@example.test>\r\nIn-Reply-To: <%s>", id, target), false, second)
	outgoing.ConfirmedSourceSender = true
	outgoing.PositiveSent = true
	outgoing.ExternalRecipient = true
	return outgoing
}

func TestEmailWorkHandlingDirectReplyClosesAncestorsNotSiblingsOrNewMail(t *testing.T) {
	assertions := assert.New(t)
	root := workActivity(1, 1, "Message-ID: <root@example.test>", true, 1)
	branch := workActivity(2, 1, "Message-ID: <branch@example.test>\r\nIn-Reply-To: <root@example.test>", true, 2)
	sibling := workActivity(3, 3, "Message-ID: <sibling@example.test>\r\nIn-Reply-To: <root@example.test>", true, 3)
	outgoing := completedReply(4, "branch@example.test", 4)
	got := store.EvaluateEmailWorkHandling([]store.EmailWorkActivity{root, branch, sibling, outgoing})
	assertions.Equal("actionable", got.State)
	assertions.Equal([]int64{3}, got.OutstandingEventIDs)
	assertions.Equal([]int64{4}, got.OutboundEvidenceIDs)
	handled := store.EvaluateEmailWorkHandling([]store.EmailWorkActivity{root, branch, outgoing})
	assertions.Equal("handled", handled.State)
	assertions.Empty(handled.OutstandingEventIDs)
	newer := workActivity(5, 1, "Message-ID: <newer@example.test>\r\nIn-Reply-To: <sent-4@example.test>", true, 5)
	reopened := store.EvaluateEmailWorkHandling([]store.EmailWorkActivity{root, branch, outgoing, newer})
	assertions.Equal("actionable", reopened.State)
	assertions.Equal([]int64{5}, reopened.OutstandingEventIDs)
}

func TestEmailWorkHandlingForwardAndDuplicateCopiesRemainOneReceivedEvent(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	incoming := workActivity(1, 1, "Message-ID: <original@example.test>", true, 1)
	otherCopy := incoming
	otherCopy.MessageID = 2
	otherCopy.SourceID = 3
	outgoing := completedReply(3, "unused@example.test", 3)
	outgoing.Linkage.Replies = nil
	outgoing.Linkage.AttachedOriginals = []string{"original@example.test"}
	before := store.EvaluateEmailWorkHandling([]store.EmailWorkActivity{otherCopy, incoming})
	assertions.Equal([]int64{1}, before.OutstandingEventIDs)
	got := store.EvaluateEmailWorkHandling([]store.EmailWorkActivity{otherCopy, outgoing, incoming})
	assertions.Equal("handled", got.State)
	assertions.Equal([]int64{3}, got.OutboundEvidenceIDs)
	// Provider Inbox/read statuses live in their own publication and cannot be
	// changed by this evaluator; underlying source copies remain in the partition.
	partition, err := store.PartitionEmailWorkMembers([]store.EmailWorkMember{otherCopy.EmailWorkMember, outgoing.EmailWorkMember, incoming.EmailWorkMember})
	requirements.NoError(err)
	assertions.Equal([][]int64{{1, 2, 3}}, partition)
}

func TestEmailWorkHandlingRequiresEveryPositiveCompletionFact(t *testing.T) {
	incoming := workActivity(1, 1, "Message-ID: <target@example.test>", true, 2)
	good := completedReply(2, "target@example.test", 3)
	for _, tc := range []struct {
		name   string
		mutate func(*store.EmailWorkActivity)
	}{
		{"unconfirmed sender", func(m *store.EmailWorkActivity) { m.ConfirmedSourceSender = false }},
		{"no positive Sent", func(m *store.EmailWorkActivity) { m.PositiveSent = false }},
		{"draft", func(m *store.EmailWorkActivity) { m.Draft = true }},
		{"self only", func(m *store.EmailWorkActivity) { m.ExternalRecipient = false }},
		{"unknown identity", func(m *store.EmailWorkActivity) { m.AttributionComplete = false }},
		{"old outgoing", func(m *store.EmailWorkActivity) { m.OccurredAt = incoming.OccurredAt.Add(-time.Second) }},
		{"same timestamp", func(m *store.EmailWorkActivity) { m.OccurredAt = incoming.OccurredAt }},
		{"missing date", func(m *store.EmailWorkActivity) { m.OccurredAt = time.Time{} }},
		{"References only", func(m *store.EmailWorkActivity) {
			m.Linkage.Replies = nil
			m.Linkage.References = []string{"target@example.test"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertions := assert.New(t)
			outgoing := good
			tc.mutate(&outgoing)
			got := store.EvaluateEmailWorkHandling([]store.EmailWorkActivity{incoming, outgoing})
			assertions.Equal("actionable", got.State)
			assertions.Equal([]int64{1}, got.OutstandingEventIDs)
			assertions.Empty(got.OutboundEvidenceIDs)
		})
	}
}

func TestEmailWorkHandlingIncompleteFactsStayVisibleAndScoped(t *testing.T) {
	assertions := assert.New(t)
	incoming := workActivity(1, 1, "Message-ID: <target@example.test>", true, 1)
	outgoing := completedReply(2, "target@example.test", 2)
	scoped := store.EvaluateEmailWorkHandling([]store.EmailWorkActivity{incoming})
	assertions.Equal("actionable", scoped.State)
	assertions.Equal([]int64{1}, scoped.OutstandingEventIDs)
	assertions.Empty(scoped.OutboundEvidenceIDs)
	incoming.Linkage.Outcome = "missing_raw"
	incomplete := store.EvaluateEmailWorkHandling([]store.EmailWorkActivity{incoming, outgoing})
	assertions.Equal("unknown", incomplete.State)
	assertions.Contains(incomplete.Reasons, "linkage_unavailable")
	// Removing a previously valid outgoing cannot leave cached handling behind.
	incoming.Linkage.Outcome = "complete"
	recovered := store.EvaluateEmailWorkHandling([]store.EmailWorkActivity{incoming})
	assertions.Equal("actionable", recovered.State)
	empty := store.EvaluateEmailWorkHandling(nil)
	assertions.Equal("unknown", empty.State)
	assertions.True(slices.Contains(empty.Reasons, "no_received_item"))
}

func TestEmailWorkHandlingLongDirectAncestry(t *testing.T) {
	assertions := assert.New(t)
	const count = 5000
	members := make([]store.EmailWorkActivity, 0, count)
	for id := 1; id <= count; id++ {
		header := fmt.Sprintf("Message-ID: <m-%d@example.test>", id)
		if id > 1 {
			header += fmt.Sprintf("\r\nIn-Reply-To: <m-%d@example.test>", id-1)
		}
		member := workActivity(int64(id), 1, header, id%2 == 1, 1)
		member.OccurredAt = time.Unix(int64(id), 0)
		if id%2 == 0 {
			member.SourceID = 2
			member.ConfirmedSourceSender = true
			member.PositiveSent = true
			member.ExternalRecipient = true
		}
		members = append(members, member)
	}
	// Reversed ingestion order cannot change causal completion. This exercises
	// thousands of real parsed direct links without a throughput/time assertion.
	slices.Reverse(members)
	got := store.EvaluateEmailWorkHandling(members)
	assertions.Equal("handled", got.State)
	assertions.Empty(got.OutstandingEventIDs)
	assertions.Len(got.OutboundEvidenceIDs, count/2)
	assertions.Equal(int64(2), got.OutboundEvidenceIDs[0])
	assertions.Equal(int64(count), got.OutboundEvidenceIDs[len(got.OutboundEvidenceIDs)-1])
}
