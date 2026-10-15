package store

import (
	"net/mail"
	"strings"
)

// EmailWorkEnvelope is an immutable address-header observation. Participant
// aliases and display names cannot substitute for the original envelope.
type EmailWorkEnvelope struct {
	Kind    string
	Address string
}

// AttributeEmailWorkActivity resolves ownership only from confirmed identities
// inside the effective source scope. Caller supplies positive native provider
// completion evidence separately; local/provider read state is irrelevant.
func AttributeEmailWorkActivity(activity EmailWorkActivity, envelopes []EmailWorkEnvelope, confirmedBySource map[int64][]string, providerComplete bool) EmailWorkActivity {
	return NewEmailWorkAttributor(confirmedBySource).Attribute(activity, envelopes, providerComplete)
}

// EmailWorkAttributor reuses the normalized confirmed-identity index for one
// effective source scope. It owns a copy; later caller mutations cannot change
// an already-published projection.
type EmailWorkAttributor struct {
	owners  map[string]bool
	sources map[int64]map[string]bool
}

func NewEmailWorkAttributor(confirmedBySource map[int64][]string) *EmailWorkAttributor {
	attributor := &EmailWorkAttributor{owners: map[string]bool{}, sources: map[int64]map[string]bool{}}
	for source, addresses := range confirmedBySource {
		index := map[string]bool{}
		for _, address := range addresses {
			if normalized := emailWorkAddress(address); normalized != "" {
				index[normalized] = true
				attributor.owners[normalized] = true
			}
		}
		attributor.sources[source] = index
	}
	return attributor
}

func (a *EmailWorkAttributor) Attribute(activity EmailWorkActivity, envelopes []EmailWorkEnvelope, providerComplete bool) EmailWorkActivity {
	activity.Incoming = false
	activity.ExternalRecipient = false
	activity.ConfirmedSourceSender = false
	activity.AttributionComplete = false
	activity.UnavailableReasons = nil
	sourceOwners := a.sources[activity.SourceID]
	var sender string
	fromCount := 0
	hasRecipient, ownedRecipient, unavailable := false, false, false
	for _, envelope := range envelopes {
		address := emailWorkAddress(envelope.Address)
		switch envelope.Kind {
		case messageSenderRole:
			fromCount++
			sender = address
			unavailable = unavailable || address == ""
		case "to", "cc", "bcc":
			hasRecipient = true
			if address == "" {
				unavailable = true
				continue
			}
			ownedRecipient = ownedRecipient || a.owners[address]
			activity.ExternalRecipient = activity.ExternalRecipient || !a.owners[address]
		}
	}
	activity.ConfirmedSourceSender = fromCount == 1 && sourceOwners[sender]
	activity.Incoming = fromCount == 1 && sender != "" && !a.owners[sender] && ownedRecipient
	identityComplete := fromCount == 1 && hasRecipient && !unavailable && len(sourceOwners) > 0
	if !identityComplete {
		activity.UnavailableReasons = append(activity.UnavailableReasons, "identity_unavailable")
	}
	if !providerComplete {
		activity.UnavailableReasons = append(activity.UnavailableReasons, "provider_evidence_unavailable")
	}
	activity.AttributionComplete = identityComplete && providerComplete
	return activity
}

func emailWorkAddress(value string) string {
	value = strings.TrimSpace(value)
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Name != "" || parsed.Address != value {
		return ""
	}
	return strings.ToLower(parsed.Address)
}
