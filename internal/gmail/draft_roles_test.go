package gmail

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestCanonicalDraftRoleDoesNotUseDisplayNames(t *testing.T) {
	assertions := assert.New(t)
	assertions.Equal("draft", SystemRoleForLabelID("DRAFT"))
	assertions.Equal("sent", SystemRoleForLabelID("SENT"))
	assertions.Empty(SystemRoleForLabelID("draft"))
	assertions.Empty(SystemRoleForLabelID("Label_custom_drafts"))
}
