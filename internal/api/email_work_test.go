package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/mime"
	"go.kenn.io/msgvault/internal/query"
)

func TestEmailWorkHTTPRowsMembersAndSavedVocabulary(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	engine, root := newExploreDuckDBFixtureWithDir(t)
	db, err := sql.Open("duckdb", "")
	requirements.NoError(err)
	defer func() { requirements.NoError(db.Close()) }()
	writeDataset := func(name, selection string) {
		path := strings.ReplaceAll(filepath.ToSlash(filepath.Join(root, name, name+".parquet")), "'", "''")
		_, err := db.Exec(fmt.Sprintf("COPY (%s) TO '%s' (FORMAT PARQUET)", selection, path))
		requirements.NoError(err)
	}
	headers := []string{"Message-ID: <original@example.test>\r\n\r\n[QA]", "Message-ID: <reply@example.test>\r\nIn-Reply-To: <original@example.test>\r\n\r\n[QA]"}
	var facts []string
	for i, raw := range headers {
		encoded, err := json.Marshal(mime.ExtractEmailLinkage([]byte(raw)))
		requirements.NoError(err)
		facts = append(facts, fmt.Sprintf("(%d::BIGINT,1::INTEGER,'complete','%s')", i+1, strings.ReplaceAll(string(encoded), "'", "''")))
	}
	writeDataset(query.DatasetEmailWorkFacts, "SELECT * FROM (VALUES "+strings.Join(facts, ",")+") f(message_id,extraction_version,outcome,facts)")
	writeDataset(query.DatasetEmailWorkSeeds, "SELECT * FROM (VALUES (1::BIGINT,'qa-root',1::BIGINT,1::BIGINT),(2::BIGINT,'qa-reply',2::BIGINT,2::BIGINT)) f(ordinal,uid,anchor_message_id,live_message_id)")
	writeDataset(query.DatasetEmailWorkIdentities, "SELECT 1::BIGINT AS source_id,'archive-a@example.com' AS address")
	writeDataset(query.DatasetEmailWorkDelivery, "SELECT * FROM (VALUES (1::BIGINT,1::BIGINT,NULL::VARCHAR,false,false,true),(2::BIGINT,1::BIGINT,NULL::VARCHAR,true,false,true)) f(message_id,source_id,provider_thread_id,native_sent,native_draft,provider_complete)")
	writeDataset("message_recipients", "SELECT * FROM (VALUES (1::BIGINT,1::BIGINT,'from','[QA]','alice@example.com','alice@example.com'),(1::BIGINT,2::BIGINT,'to','[QA]','archive-a@example.com','archive-a@example.com'),(2::BIGINT,1::BIGINT,'from','[QA]','archive-a@example.com','archive-a@example.com'),(2::BIGINT,2::BIGINT,'to','[QA]','delegate@example.test','delegate@example.test')) f(message_id,participant_id,recipient_type,display_name,email_address,envelope_address)")
	state, err := query.ReadCacheSyncState(root)
	requirements.NoError(err)
	state.DatasetFingerprint, err = query.CacheDatasetFingerprint(root)
	requirements.NoError(err)
	state.PublishedAt = state.PublishedAt.Add(time.Second)
	encoded, err := json.Marshal(state)
	requirements.NoError(err)
	requirements.NoError(os.WriteFile(query.CacheStatePath(root), encoded, 0o600))
	srv := newTestServerWithEngine(t, engine)
	response := postExploreJSON(t, srv, "/api/v1/explore", `{"filters":[{"dimension":"source","values":["1"]},{"dimension":"row_unit","values":["email_work_item"]}],"limit":10}`)
	requirements.Equal(http.StatusOK, response.Code, response.Body.String())
	var rows ExploreHTTPResponse
	requirements.NoError(json.Unmarshal(response.Body.Bytes(), &rows))
	requirements.Len(rows.Rows, 1)
	assertions.Equal(query.EntryEmailWork, rows.Rows[0].Kind)
	requirements.NotNil(rows.Rows[0].EmailWork)
	assertions.Equal("handled", rows.Rows[0].EmailWork.State)
	filtered := postExploreJSON(t, srv, "/api/v1/explore", `{"filters":[{"dimension":"source","values":["1"]},{"dimension":"row_unit","values":["email_work_item"]},{"dimension":"handling_state","values":["actionable","unknown"]}]}`)
	requirements.Equal(http.StatusOK, filtered.Code, filtered.Body.String())
	var pending ExploreHTTPResponse
	requirements.NoError(json.Unmarshal(filtered.Body.Bytes(), &pending))
	assertions.Empty(pending.Rows, "completed work is excluded independently of mailbox read state")

	members := postExploreJSON(t, srv, "/api/v1/explore/email-work/members", fmt.Sprintf(`{"uid":"qa-reply","predicate":{"filters":[{"dimension":"source","values":["1"]}]},"cache_revision":%q}`, rows.CacheRevision))
	requirements.Equal(http.StatusOK, members.Code, members.Body.String())
	var family query.EmailWorkMembersResponse
	requirements.NoError(json.Unmarshal(members.Body.Bytes(), &family))
	assertions.Equal("qa-root", family.UID)
	requirements.Len(family.Members, 2)
	assertions.Equal("original@example.test", family.Members[0].RFCMessageID)
	assertions.Equal([]string{"original@example.test"}, family.Members[1].ReplyIDs)
	stale := postExploreJSON(t, srv, "/api/v1/explore/email-work/members", `{"uid":"qa-root","predicate":{},"cache_revision":"old"}`)
	assertions.Equal(http.StatusConflict, stale.Code, stale.Body.String())
	unavailable := postExploreJSON(t, srv, "/api/v1/explore/email-work/members", `{"uid":"qa-root","predicate":{"filters":[{"dimension":"source","values":["2"]}]}}`)
	assertions.Equal(http.StatusNotFound, unavailable.Code, unavailable.Body.String())
}
