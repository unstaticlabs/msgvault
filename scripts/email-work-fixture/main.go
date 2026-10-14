// Command email-work-fixture creates a new, synthetic local archive for browser QA.
// It refuses an existing destination and has no provider configuration or calls.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/authz"
	"go.kenn.io/msgvault/internal/store"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: email-work-fixture NEW_DATA_DIR")
		os.Exit(2)
	}
	if err := seed(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type fixtureMessage struct {
	source                         int
	key, rfc, reply, subject, body string
	sent, unread, missing          bool
}

func seed(destination string) error {
	destination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	// A new directory is the ownership proof; existing archives are never opened.
	// #nosec G703 -- explicit CLI destination; exclusive creation refuses every existing archive or symlink.
	if err = os.Mkdir(destination, 0o700); err != nil {
		return fmt.Errorf("create new QA destination: %w", err)
	}
	st, err := store.Open(filepath.Join(destination, "msgvault.db"))
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	if err = st.InitSchema(); err != nil {
		return err
	}
	ctx := context.Background()
	sources := make([]int64, 3)
	addresses := []string{"qa-field@example.test", "qa-office@example.test", "qa-backup@example.test"}
	for i, address := range addresses {
		source, createErr := st.GetOrCreateSource("gmail", address)
		if createErr != nil {
			return createErr
		}
		sources[i] = source.ID
		if err = st.AddAccountIdentity(source.ID, address, "synthetic_qa"); err != nil {
			return err
		}
	}
	external := "qa-collaborator@example.test"
	scenarios := []fixtureMessage{
		{source: 0, key: "request", rfc: "visit@example.test", subject: "[QA] Field visit confirmation", body: "Please confirm the synthetic field visit.", unread: true},
		{source: 2, key: "copy", rfc: "visit@example.test", subject: "[QA] Field visit confirmation", body: "Please confirm the synthetic field visit.", unread: true},
		{source: 1, key: "reply", rfc: "visit-reply@example.test", reply: "visit@example.test", subject: "[QA] Field visit confirmation", body: "Confirmed. This direct reply has trusted message identity.", sent: true},
		{source: 0, key: "new-inbound", rfc: "visit-update@example.test", reply: "visit-reply@example.test", subject: "[QA] Field visit confirmation", body: "A new question arrived after the reply. This item needs attention again.", unread: true},
		{source: 0, key: "closed-request", rfc: "closed@example.test", subject: "[QA] Document review complete", body: "Please review this synthetic document.", unread: true},
		{source: 1, key: "closed-reply", rfc: "closed-reply@example.test", reply: "closed@example.test", subject: "[QA] Document review complete", body: "Reviewed. Trusted direct-reply evidence closes the addressed request.", sent: true},
		{source: 0, key: "inline-request", rfc: "inline@example.test", subject: "[QA] Arrange site access", body: "Please arrange synthetic site access.", unread: true},
		{source: 1, key: "inline-forward", rfc: "forward@example.test", subject: "[QA] Arrange site access", body: "Forwarded request: please arrange synthetic site access. This ordinary inline forward has no trusted reply reference. Link copies explicitly when appropriate.", sent: true},
		{source: 2, key: "unrelated", rfc: "unrelated@example.test", subject: "[QA] Arrange site access", body: "Same subject, different request. It must stay separate.", unread: true},
		{source: 2, key: "unknown", subject: "[QA] Incomplete archived evidence", body: "This synthetic legacy copy has no archived raw MIME. It remains visible with an explanation.", missing: true, unread: true},
	}
	records := map[string]string{}
	for i, scenario := range scenarios {
		sourceID := sources[scenario.source]
		senderAddress, recipientAddress := external, addresses[scenario.source]
		if scenario.sent {
			senderAddress, recipientAddress = addresses[scenario.source], external
		}
		sender, createErr := st.EnsureParticipant(senderAddress, "[QA] Sender", "example.test")
		if createErr != nil {
			return createErr
		}
		recipient, createErr := st.EnsureParticipant(recipientAddress, "[QA] Recipient", "example.test")
		if createErr != nil {
			return createErr
		}
		conversation, createErr := st.EnsureConversation(sourceID, scenario.key, scenario.subject)
		if createErr != nil {
			return createErr
		}
		labelNames := []string{"INBOX"}
		if scenario.sent {
			labelNames = []string{"SENT"}
		}
		if scenario.unread {
			labelNames = append(labelNames, "UNREAD")
		}
		var labels []int64
		for _, name := range labelNames {
			label, createErr := st.EnsureLabel(sourceID, name, name, "system")
			if createErr != nil {
				return createErr
			}
			labels = append(labels, label)
		}
		date := time.Date(2026, 10, 14, 9, i, 0, 0, time.UTC)
		var raw []byte
		if !scenario.missing {
			headers := []string{"Message-ID: <" + scenario.rfc + ">", "From: " + senderAddress, "To: " + recipientAddress, "Subject: " + scenario.subject, "Date: " + date.Format(time.RFC1123Z), "Content-Type: text/plain; charset=utf-8"}
			if scenario.reply != "" {
				headers = append(headers, "In-Reply-To: <"+scenario.reply+">", "References: <"+scenario.reply+">")
			}
			raw = []byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + scenario.body)
		}
		id, persistErr := st.PersistMessage(&store.MessagePersistData{Message: &store.Message{SourceID: sourceID, ConversationID: conversation, SourceMessageID: scenario.key, MessageType: "email", Subject: sql.NullString{String: scenario.subject, Valid: true}, Snippet: sql.NullString{String: scenario.body, Valid: true}, SenderID: sql.NullInt64{Int64: sender, Valid: true}, SentAt: sql.NullTime{Time: date, Valid: true}, SizeEstimate: int64(len(raw))}, RawMIME: raw, BodyText: sql.NullString{String: scenario.body, Valid: true}, LabelIDs: labels, Recipients: []store.RecipientSet{{Type: "from", ParticipantIDs: []int64{sender}, EmailAddresses: []string{senderAddress}, DisplayNames: []string{"[QA] Sender"}}, {Type: "to", ParticipantIDs: []int64{recipient}, EmailAddresses: []string{recipientAddress}, DisplayNames: []string{"[QA] Recipient"}}}})
		if persistErr != nil {
			return persistErr
		}
		seed, seedErr := st.EmailWorkSeedForMessage(ctx, id)
		if seedErr != nil {
			return seedErr
		}
		records[scenario.key] = seed.UID
		if scenario.key == "request" {
			if _, err = st.DB().Exec("UPDATE messages SET is_read=FALSE WHERE id=?", id); err != nil {
				return err
			}
		}
	}
	if _, err = st.BackfillFTS(nil); err != nil {
		return err
	}
	for _, role := range []authz.Role{authz.RoleMember, authz.RoleViewer} {
		address := fmt.Sprintf("qa-%s@example.test", role)
		user, userErr := st.RecordUserLogin(ctx, store.UserLogin{Issuer: "https://synthetic-idp.example.test", Subject: string(role), Email: address, DisplayName: "[QA] " + string(role), Role: string(role)})
		if userErr != nil {
			return userErr
		}
		if err = st.SetUserSources(ctx, user.ID, sources); err != nil {
			return err
		}
	}
	encoded, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	// #nosec G703 -- fixed filename beneath the exclusively created QA directory.
	if err = os.WriteFile(filepath.Join(destination, "qa-work-items.json"), encoded, 0o600); err != nil {
		return err
	}
	configuration := `[server]
api_port = 64831
bind_addr = "127.0.0.1"
api_key = "synthetic-work-admin"
daemon_auto_restart = "never"
[analytics]
engine = "duckdb"
auto_build_cache = false
[vector]
enabled = false
[[auth.api_keys]]
name = "qa-member"
key = "synthetic-work-member"
role = "member"
user = "qa-member@example.test"
[[auth.api_keys]]
name = "qa-viewer"
key = "synthetic-work-viewer"
role = "viewer"
user = "qa-viewer@example.test"
`
	// #nosec G703 -- fixed filename beneath the exclusively created QA directory; synthetic loopback-only configuration.
	return os.WriteFile(filepath.Join(destination, "config.toml"), []byte(configuration), 0o600)
}
