package store

import (
	"fmt"
	"slices"
	"time"

	"go.kenn.io/msgvault/internal/mime"
)

const EmailWorkUnknown = "unknown"

// EmailWorkActivity is a source-scoped published activity fact. PositiveSent
// comes from native Sent membership, never messages.is_sent. Incoming is
// resolved separately from confirmed owner identities in the selected scope.
type EmailWorkActivity struct {
	EmailWorkMember

	OccurredAt            time.Time
	Incoming              bool
	AttributionComplete   bool
	UnavailableReasons    []string
	ConfirmedSourceSender bool
	PositiveSent          bool
	Draft                 bool
	ExternalRecipient     bool
	LocalHandled          bool
}

// EmailWorkHandling explains outstanding received events and actual outbound
// evidence. Provider read/unread and Inbox flags do not enter this calculation.
type EmailWorkHandling struct {
	State               string
	OutstandingEventIDs []int64
	OutboundEvidenceIDs []int64
	Reasons             []string
}

type emailReceivedEvent struct {
	anchor     int64
	incoming   bool
	hasTime    bool
	occurredAt time.Time
	parents    []string
}

// EvaluateEmailWorkHandling receives one already-authorized scoped component.
// Grouping references and provider threads do not confer handling authority.
func EvaluateEmailWorkHandling(members []EmailWorkActivity) EmailWorkHandling {
	result := EmailWorkHandling{State: EmailWorkUnknown, OutstandingEventIDs: []int64{}, OutboundEvidenceIDs: []int64{}, Reasons: []string{}}
	events := map[string]*emailReceivedEvent{}
	identityEvent := map[string]string{}
	memberEvent := map[int64]string{}
	incomplete := false
	reason := func(value string) {
		if !slices.Contains(result.Reasons, value) {
			result.Reasons = append(result.Reasons, value)
		}
	}
	for _, member := range members {
		key := fmt.Sprintf("message:%d", member.MessageID)
		if member.Linkage.Version == mime.EmailLinkageVersion && member.Linkage.MessageID != "" {
			key = "rfc:" + member.Linkage.MessageID
			identityEvent[member.Linkage.MessageID] = key
		}
		memberEvent[member.MessageID] = key
		event, found := events[key]
		if !found {
			event = &emailReceivedEvent{anchor: member.MessageID, hasTime: true}
			events[key] = event
		}
		event.anchor = min(event.anchor, member.MessageID)
		event.incoming = event.incoming || member.Incoming || member.LocalHandled
		if member.OccurredAt.IsZero() {
			event.hasTime = false
		} else if member.OccurredAt.After(event.occurredAt) {
			event.occurredAt = member.OccurredAt
		}
		if !member.AttributionComplete && !member.LocalHandled {
			incomplete = true
			if len(member.UnavailableReasons) == 0 {
				reason("identity_unavailable")
			} else {
				for _, unavailable := range member.UnavailableReasons {
					reason(unavailable)
				}
			}
		}
		if (member.Linkage.Version != mime.EmailLinkageVersion || member.Linkage.Outcome != "complete") && !member.LocalHandled {
			incomplete = true
			reason("linkage_unavailable")
		}
		// Only direct verified replies establish handling ancestry. References may
		// group sibling branches, but cannot close them as a side effect.
		if member.Linkage.Version == mime.EmailLinkageVersion && member.Linkage.Outcome == "complete" {
			for _, parent := range member.Linkage.Replies {
				if !slices.Contains(event.parents, parent) {
					event.parents = append(event.parents, parent)
				}
			}
		}
	}
	closed := map[string]bool{}
	for _, member := range members {
		if member.LocalHandled {
			closed[memberEvent[member.MessageID]] = true
			reason("local_confirmation")
		}
	}
	// Process completed activity chronologically. A valid direct ancestry is
	// walked once; later replies cannot create quadratic rescans of long threads.
	walked := map[string]bool{}
	outbounds := slices.Clone(members)
	slices.SortFunc(outbounds, func(a, b EmailWorkActivity) int {
		if order := a.OccurredAt.Compare(b.OccurredAt); order != 0 {
			return order
		}
		if a.MessageID < b.MessageID {
			return -1
		}
		if a.MessageID > b.MessageID {
			return 1
		}
		return 0
	})
	for _, outbound := range outbounds {
		if !outbound.AttributionComplete || !outbound.ConfirmedSourceSender || !outbound.PositiveSent || outbound.Draft || !outbound.ExternalRecipient || outbound.OccurredAt.IsZero() || outbound.Linkage.Version != mime.EmailLinkageVersion || outbound.Linkage.Outcome != "complete" {
			continue
		}
		targetIDs := slices.Concat(outbound.Linkage.Replies, outbound.Linkage.AttachedOriginals)
		pending := make([]string, 0, len(targetIDs))
		for _, id := range targetIDs {
			if key, found := identityEvent[id]; found {
				pending = append(pending, key)
			}
		}
		visited := map[string]bool{}
		contributed := false
		for len(pending) > 0 {
			key := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if visited[key] || key == memberEvent[outbound.MessageID] {
				continue
			}
			visited[key] = true
			event := events[key]
			if walked[key] {
				continue
			}
			if !event.hasTime || !outbound.OccurredAt.After(event.occurredAt) {
				reason("chronology_unavailable")
				continue
			}
			walked[key] = true
			if event.incoming && !closed[key] {
				closed[key] = true
				contributed = true
			}
			for _, parent := range event.parents {
				if ancestor, found := identityEvent[parent]; found {
					ancestorEvent := events[ancestor]
					if !ancestorEvent.hasTime || !event.occurredAt.After(ancestorEvent.occurredAt) {
						reason("chronology_unavailable")
						continue
					}
					pending = append(pending, ancestor)
				}
			}
		}
		if contributed {
			result.OutboundEvidenceIDs = append(result.OutboundEvidenceIDs, outbound.MessageID)
		}
	}
	received := 0
	for key, event := range events {
		if !event.incoming {
			continue
		}
		received++
		if !closed[key] {
			result.OutstandingEventIDs = append(result.OutstandingEventIDs, event.anchor)
		}
	}
	switch {
	case len(result.OutstandingEventIDs) > 0:
		result.State = "actionable"
	case received == 0:
		reason("no_received_item")
	case !incomplete:
		result.State = "handled"
	}
	slices.Sort(result.OutstandingEventIDs)
	slices.Sort(result.OutboundEvidenceIDs)
	slices.Sort(result.Reasons)
	return result
}
