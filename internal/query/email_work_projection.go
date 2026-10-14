package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/store"
)

// These engine-owned relations are a derived projection of one immutable
// publication and effective authority, never a second authoritative cache.
type emailWorkRelations struct{ members, components, aliases, actions string }

func (r emailWorkRelations) names() []string {
	return []string{r.members, r.components, r.aliases, r.actions}
}

func (e *DuckDBEngine) materializeEmailWorkPopulation(ctx context.Context, p *emailWorkPopulation) (err error) {
	base := fmt.Sprintf("_email_work_projection_%d", e.tempTableSeq.Add(1))
	p.relations = emailWorkRelations{base + "_members", base + "_components", base + "_aliases", base + "_actions"}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		"CREATE TEMP TABLE " + p.relations.members + " (message_id BIGINT PRIMARY KEY,component BIGINT,member_ordinal BIGINT,work_key VARCHAR,handling_state VARCHAR,uid VARCHAR,source_id BIGINT,activity VARCHAR)",
		"CREATE TEMP TABLE " + p.relations.components + " (component BIGINT PRIMARY KEY,uid VARCHAR UNIQUE,member_count BIGINT,summary VARCHAR,handling VARCHAR)",
		"CREATE TEMP TABLE " + p.relations.aliases + " (uid VARCHAR PRIMARY KEY,ordinal BIGINT,anchor_message_id BIGINT,live_message_id BIGINT,component BIGINT,source_id BIGINT)",
		"CREATE TEMP TABLE " + p.relations.actions + " (component BIGINT,uid VARCHAR,payload VARCHAR,PRIMARY KEY(component,uid))",
	} {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	insert := func(table string, width int, visit func(func(...any) error) error) error {
		values := []string{}
		args := []any{}
		flush := func() error {
			if len(values) == 0 {
				return nil
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES "+strings.Join(values, ","), args...)
			values = nil
			args = nil
			return err
		}
		emit := func(row ...any) error {
			values = append(values, "("+strings.TrimSuffix(strings.Repeat("?,", width), ",")+")")
			args = append(args, row...)
			if len(values) == 500 {
				return flush()
			}
			return nil
		}
		if err := visit(emit); err != nil {
			return err
		}
		return flush()
	}
	liveSeeds := map[int64]string{}
	for _, seed := range p.seeds {
		if seed.LiveMessageID.Valid {
			liveSeeds[seed.LiveMessageID.Int64] = seed.UID
		}
	}
	if err = insert(p.relations.members, 8, func(emit func(...any) error) error {
		for component, ids := range p.components {
			canonical := p.canonicalSeeds[component].UID
			if canonical == "" {
				continue
			}
			for ordinal, id := range ids {
				// Chronology was evaluated before publication; copy dates remain in
				// analytical_entries and are read there with the selected member page.
				// Do not introduce a JSON timestamp-range gate for the whole archive.
				attributed := p.activities[id]
				attributed.OccurredAt = time.Time{}
				activity, err := json.Marshal(attributed)
				if err != nil {
					return err
				}
				if err := emit(id, component, ordinal, "email-work:"+canonical, p.handling[component].State, liveSeeds[id], p.activities[id].SourceID, string(activity)); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err = insert(p.relations.components, 5, func(emit func(...any) error) error {
		for component, ids := range p.components {
			uid := p.canonicalSeeds[component].UID
			if uid == "" {
				continue
			}
			summary, err := json.Marshal(emailWorkSummary(p, "email-work:"+uid))
			if err != nil {
				return err
			}
			handling, err := json.Marshal(p.handling[component])
			if err != nil {
				return err
			}
			if err := emit(component, uid, len(ids), string(summary), string(handling)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err = insert(p.relations.aliases, 6, func(emit func(...any) error) error {
		for _, seed := range p.seeds {
			var component, source any
			if value, found := p.componentForMessage[seed.LiveMessageID.Int64]; seed.LiveMessageID.Valid && found {
				component = value
				source = p.activities[seed.LiveMessageID.Int64].SourceID
			}
			if err := emit(seed.UID, seed.Ordinal, seed.AnchorMessageID, seed.LiveMessageID, component, source); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err = insert(p.relations.actions, 3, func(emit func(...any) error) error {
		for _, action := range p.localActions {
			payload, err := json.Marshal(action)
			if err != nil {
				return err
			}
			components := []int{}
			for _, target := range action.Targets {
				seed := p.seeds[target]
				if component, found := p.componentForMessage[seed.LiveMessageID.Int64]; seed.LiveMessageID.Valid && found {
					components = append(components, component)
				}
			}
			slices.Sort(components)
			for _, component := range slices.Compact(components) {
				if err := emit(component, action.UID, string(payload)); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "CREATE INDEX "+base+"_member_component ON "+p.relations.members+"(component)"); err != nil {
		return err
	}
	return tx.Commit()
}

func (e *DuckDBEngine) dropEmailWorkRelations(p *emailWorkPopulation) error {
	tx, err := e.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, name := range p.relations.names() {
		if name != "" {
			if _, err := tx.Exec("DROP TABLE IF EXISTS " + name); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (p *emailWorkPopulation) releaseGoFacts() {
	p.activities = nil
	p.components = nil
	p.seeds = nil
	p.componentForMessage = nil
	p.handling = nil
	p.canonicalSeeds = nil
	p.localActions = nil
	p.cost = 0
}

type emailWorkAlias struct {
	seed      store.EmailWorkSeed
	component int
	sourceID  int64
}

func (e *DuckDBEngine) emailWorkAliasLocked(ctx context.Context, p *emailWorkPopulation, uid string) (emailWorkAlias, error) {
	var alias emailWorkAlias
	var component, source sql.NullInt64
	err := e.db.QueryRowContext(ctx, "SELECT ordinal,anchor_message_id,live_message_id,component,source_id FROM "+p.relations.aliases+" WHERE uid=?", uid).Scan(&alias.seed.Ordinal, &alias.seed.AnchorMessageID, &alias.seed.LiveMessageID, &component, &source)
	if errors.Is(err, sql.ErrNoRows) {
		return alias, store.ErrEmailWorkAliasMissing
	}
	if err != nil {
		return alias, err
	}
	alias.seed.UID = uid
	if !alias.seed.LiveMessageID.Valid {
		return alias, store.ErrEmailWorkAliasGone
	}
	if !component.Valid {
		return alias, store.ErrEmailWorkAliasOutOfScope
	}
	alias.component = int(component.Int64)
	alias.sourceID = source.Int64
	return alias, nil
}

func (e *DuckDBEngine) emailWorkSummaryLocked(ctx context.Context, p *emailWorkPopulation, key string) (*EmailWorkSummary, error) {
	if p == nil || !strings.HasPrefix(key, "email-work:") {
		return nil, fmt.Errorf("%w: a related-email summary key is required", ErrInvalidExploreRequest)
	}
	var encoded string
	err := e.db.QueryRowContext(ctx, "SELECT summary FROM "+p.relations.components+" WHERE uid=?", strings.TrimPrefix(key, "email-work:")).Scan(&encoded)
	if err != nil {
		return nil, err
	}
	var summary EmailWorkSummary
	err = json.Unmarshal([]byte(encoded), &summary)
	return &summary, err
}

func (e *DuckDBEngine) validateEmailWorkSelectionLocked(ctx context.Context, p *emailWorkPopulation, keys []string) error {
	for _, key := range keys {
		if !strings.HasPrefix(key, "email-work:") {
			continue
		}
		summary, err := e.emailWorkSummaryLocked(ctx, p, key)
		if errors.Is(err, sql.ErrNoRows) || summary == nil {
			return ErrEmailWorkRevisionChanged
		}
		if err != nil {
			return err
		}
	}
	return nil
}
