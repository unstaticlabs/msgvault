package store_test

import (
	"slices"
	"testing"
	"testing/quick"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/mime"
	"go.kenn.io/msgvault/internal/store"
)

func workMember(id, source int64, rawHeaders string) store.EmailWorkMember {
	return store.EmailWorkMember{MessageID: id, SourceID: source, Linkage: mime.ExtractEmailLinkage([]byte(rawHeaders + "\r\n\r\n[QA] body"))}
}

func TestEmailWorkPartitionPreservesCopiesAndExplicitForward(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	members := []store.EmailWorkMember{
		workMember(1, 1, "Message-ID: <original@example.test>\r\nSubject: [QA] Same subject"),
		workMember(2, 2, "Message-ID: <original@example.test>\r\nSubject: [QA] Same subject"),
		workMember(3, 3, "Message-ID: <reply@example.test>\r\nIn-Reply-To: <original@example.test>"),
		workMember(4, 1, "Message-ID: <unrelated@example.test>\r\nSubject: [QA] Same subject"),
	}
	forwarded := workMember(5, 2, "Message-ID: <forward@example.test>")
	forwarded.Linkage.AttachedOriginals = []string{"original@example.test"}
	members = append(members, forwarded)
	got, err := store.PartitionEmailWorkMembers(members)
	requirements.NoError(err)
	assertions.Equal([][]int64{{1, 2, 3, 5}, {4}}, got)
	// Removing the forward's actual linkage restores its independent row.
	members[4].Linkage.AttachedOriginals = nil
	got, err = store.PartitionEmailWorkMembers(members)
	requirements.NoError(err)
	assertions.Equal([][]int64{{1, 2, 3}, {4}, {5}}, got)
}

func TestEmailWorkPartitionDoesNotUseExcludedIntermediate(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	a := workMember(1, 1, "Message-ID: <a@example.test>\r\nIn-Reply-To: <b@example.test>")
	b := workMember(2, 2, "Message-ID: <b@example.test>\r\nIn-Reply-To: <c@example.test>")
	c := workMember(3, 3, "Message-ID: <c@example.test>")
	full, err := store.PartitionEmailWorkMembers([]store.EmailWorkMember{a, b, c})
	requirements.NoError(err)
	assertions.Equal([][]int64{{1, 2, 3}}, full)
	scoped, err := store.PartitionEmailWorkMembers([]store.EmailWorkMember{a, c})
	requirements.NoError(err)
	assertions.Equal([][]int64{{1}, {3}}, scoped)
}

func TestEmailWorkPartitionProviderThreadsStaySourcePinned(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	a := workMember(1, 1, "Message-ID: <a@example.test>")
	a.ProviderThreadID = "same-provider-token"
	b := workMember(2, 1, "Message-ID: <b@example.test>")
	b.ProviderThreadID = "same-provider-token"
	c := workMember(3, 2, "Message-ID: <c@example.test>")
	c.ProviderThreadID = "same-provider-token"
	got, err := store.PartitionEmailWorkMembers([]store.EmailWorkMember{a, b, c})
	requirements.NoError(err)
	assertions.Equal([][]int64{{1, 2}, {3}}, got)
}

func TestEmailWorkPartitionCyclesMissingParentsAndOrderConservation(t *testing.T) {
	members := []store.EmailWorkMember{
		workMember(1, 1, "Message-ID: <a@example.test>\r\nIn-Reply-To: <b@example.test>"),
		workMember(2, 2, "Message-ID: <b@example.test>\r\nIn-Reply-To: <a@example.test>"),
		workMember(3, 3, "Message-ID: <c@example.test>\r\nReferences: <not-archived@example.test>"),
		workMember(4, 2, "Message-ID: <d@example.test>\r\nReferences: <not-archived@example.test>"),
		workMember(5, 1, "Subject: [QA] no identity"),
	}
	want := [][]int64{{1, 2}, {3, 4}, {5}}
	require.NoError(t, quick.Check(func(seed int64) bool {
		reordered := slices.Clone(members)
		// The generated seed chooses a deterministic permutation; no random
		// identifiers or security tokens are being created by this property.
		for i := len(reordered) - 1; i > 0; i-- {
			j := int(uint64(seed) >> uint(i*8) % uint64(i+1))
			reordered[i], reordered[j] = reordered[j], reordered[i]
		}
		got, err := store.PartitionEmailWorkMembers(reordered)
		if err != nil || len(got) != len(want) {
			return false
		}
		for i := range want {
			if !slices.Equal(want[i], got[i]) {
				return false
			}
		}
		return true
	}, &quick.Config{MaxCount: 500}))
}
