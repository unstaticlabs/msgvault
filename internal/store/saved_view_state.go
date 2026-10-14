package store

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/explorecatalog"
)

// Canonical executable vocabulary is owned by the persistence boundary, as in
// upstream Saved Views. MCP and daemon execution share this validation.
var (
	// SavedViewFilterFields are the Explore filter dimensions a v1 definition may name.
	// The legacy aliases source_id and participant_id are also accepted and
	// normalized by SavedViewFilterDimension.
	SavedViewFilterFields = []string{"source", "participant", "domain", "message_type", "after", "before", "deletion"}
	// SavedViewFilterAliases map legacy v1 filter field names onto Explore dimensions.
	SavedViewFilterAliases   = map[string]string{"source_id": "source", "participant_id": "participant"}
	SavedViewFilterOperators = []string{"eq", "in"}
	SavedViewSearchModes     = []string{"full_text", "semantic", "hybrid"}
	SavedViewPresentations   = []string{"table", "timeline", "files"}
	SavedViewSortFields      = []string{"occurred_at"}
	SavedViewSortDirections  = []string{"desc"}
	SavedViewColumns         = []string{"kind", "people", "title", "excerpt", "time", "attachments", "size"}
)

// SavedViewFilterDimension resolves the legacy source and participant aliases.
func SavedViewFilterDimension(field string) string {
	if dimension, ok := SavedViewFilterAliases[field]; ok {
		return dimension
	}
	return field
}

// ValidateSavedViewState rejects definitions the native Explore path cannot execute.
func ValidateSavedViewState(state SavedViewStateEnvelope) error {
	if err := validateSavedViewState(state); err != nil {
		return fmt.Errorf("%w: %w", ErrSavedViewInvalidState, err)
	}
	return nil
}

func validateSavedViewState(state SavedViewStateEnvelope) error {
	seen := map[string]bool{}
	for i, filter := range state.Filters {
		if !slices.Contains(SavedViewFilterFields, SavedViewFilterDimension(filter.Field)) {
			return fmt.Errorf("filters[%d].field %q is not executable", i, filter.Field)
		}
		if !slices.Contains(SavedViewFilterOperators, filter.Operator) {
			return fmt.Errorf("filters[%d].operator must be eq or in", i)
		}
		if len(filter.Values) == 0 {
			return fmt.Errorf("filters[%d].values must not be empty", i)
		}
		for j, value := range filter.Values {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("filters[%d].values[%d] must not be empty", i, j)
			}
		}

		dimension := SavedViewFilterDimension(filter.Field)
		if dimension != "participant" && dimension != "domain" {
			if seen[dimension] {
				return fmt.Errorf("filter dimension %q may appear only once", dimension)
			}
			seen[dimension] = true
		}
		for _, value := range filter.Values {
			switch dimension {
			case "source", "participant":
				id, err := strconv.ParseInt(value, 10, 64)
				if err != nil || id < 1 {
					return fmt.Errorf("filters[%d] %s requires positive integer IDs; resolve email addresses to participant IDs first", i, dimension)
				}
			case "after", "before":
				if len(filter.Values) != 1 {
					return fmt.Errorf("filters[%d] %s requires one timestamp", i, dimension)
				}
				if _, err := time.Parse(time.RFC3339, value); err != nil {
					return fmt.Errorf("filters[%d] %s requires an RFC3339 timestamp", i, dimension)
				}
			case "deletion":
				if len(filter.Values) != 1 || (value != "active" && value != "deleted") {
					return fmt.Errorf("filters[%d] deletion accepts active or deleted", i)
				}
			}
		}
	}
	if state.SearchMode != "" && !slices.Contains(SavedViewSearchModes, state.SearchMode) {
		return fmt.Errorf("search_mode %q is not supported", state.SearchMode)
	}
	for i, grouping := range state.Grouping {
		if !explorecatalog.IsGroupingDimension(grouping) {
			return fmt.Errorf("grouping[%d] %q is not supported", i, grouping)
		}
	}
	if state.Presentation != "" && !slices.Contains(SavedViewPresentations, state.Presentation) {
		return fmt.Errorf("presentation %q is not supported", state.Presentation)
	}
	for i, sort := range state.Sort {
		if !slices.Contains(SavedViewSortFields, sort.Field) || !slices.Contains(SavedViewSortDirections, sort.Direction) {
			return fmt.Errorf("sort[%d] must be occurred_at descending", i)
		}
	}
	for i, column := range state.Columns {
		if !slices.Contains(SavedViewColumns, column) {
			return fmt.Errorf("columns[%d] %q is not supported", i, column)
		}
	}
	return nil
}
