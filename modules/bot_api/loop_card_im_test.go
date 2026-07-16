//go:build pilote2e

package bot_api

// This file exercises the complete Loop origin-card path against a real
// WuKongIM instance. It deliberately lives beside the Bot API implementation
// (instead of testing only cardtmpl) so auth, group permission, structured
// payload construction, client_msg_no deduplication, card_seq editing and IM
// persistence are covered by one repeatable test.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-lib/common"
	"github.com/Mininglamp-OSS/octo-lib/config"
	"github.com/Mininglamp-OSS/octo-lib/pkg/util"
	"github.com/Mininglamp-OSS/octo-lib/testutil"
	"github.com/Mininglamp-OSS/octo-server/pkg/cardmsg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoopCardGroupLifecyclePersistsInWuKongIM(t *testing.T) {
	skipWithoutIMBot(t)
	t.Setenv(cardmsg.EnvEnabled, "true")
	t.Setenv("OCTO_MASTER_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("OCTO_USER_API_KEY_SECRET", "0123456789abcdef0123456789abcdef")

	s, ctx := testutil.NewTestServer()
	require.NoError(t, testutil.CleanAllTables(ctx))
	defer func() { _ = testutil.CleanAllTables(ctx) }()
	ctx.GetConfig().External.WebLoginURL = "https://im.example.com/login"

	suffix := time.Now().UnixNano()
	spaceID := fmt.Sprintf("sp_loop_e2e_%d", suffix)
	groupNo := fmt.Sprintf("g_loop_e2e_%d", suffix)
	botID := fmt.Sprintf("bot_loop_e2e_%d", suffix)
	botToken := fmt.Sprintf("bf_loop_e2e_%d", suffix)
	reviewerUID := fmt.Sprintf("uid_loop_reviewer_%d", suffix)
	observerUID := fmt.Sprintf("uid_loop_observer_%d", suffix)
	issueID := "4fe20b76-7fd5-4ba2-b522-ded8d0f31f33"
	workspaceID := "2224bce3-6c16-418f-9f89-4547c44bc033"

	seedLoopCardGroup(t, ctx, spaceID, groupNo, botID, botToken, reviewerUID, observerUID)
	require.NoError(t, ctx.IMCreateOrUpdateChannel(&config.ChannelCreateReq{
		ChannelID: groupNo, ChannelType: common.ChannelTypeGroup.Uint8(),
		Subscribers: []string{botID, reviewerUID, observerUID},
	}), "create the real WuKongIM group channel")

	post := func(path string, body map[string]interface{}) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+botToken)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.GetRoute().ServeHTTP(w, req)
		return w
	}

	activeLoop := map[string]interface{}{
		"variant": "active", "issue_id": issueID, "workspace_id": workspaceID,
		"identifier": "LOOP-42", "title": "完成 Web Adaptive Cards",
		"summary": "把 Loop 的关键进展同步回来源群。", "lifecycle_status": "in_review",
		"priority": "high", "assignee_name": "Octo Agent", "due_date": "2026-07-20",
		"progress": "真实群聊链路正在验收。", "revision": 1,
		"updated_at": "2026-07-15T10:30:00+08:00",
		"confirmation": map[string]interface{}{
			"id": "confirmation-1", "reviewer_uid": reviewerUID, "prompt": "请确认是否可以上线。",
		},
	}
	firstClientMsgNo := fmt.Sprintf("loop:%s:active:1", issueID)
	firstReq := map[string]interface{}{
		"channel_id": groupNo, "channel_type": common.ChannelTypeGroup.Uint8(),
		"client_msg_no": firstClientMsgNo, "mention_reviewer": true, "card_seq": 1,
		"loop": activeLoop,
	}

	first := post("/v1/bot/cards/loop/send", firstReq)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	var firstResp config.MsgSendResp
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &firstResp))
	require.NotZero(t, firstResp.MessageID)

	firstMessage := waitForLoopMessage(t, ctx, groupNo, botID, firstResp.MessageID)
	require.Equal(t, firstClientMsgNo, firstMessage.ClientMsgNo)
	firstPayload := decodeLoopPayload(t, firstMessage.Payload)
	assert.Equal(t, cardmsg.ProfileV2, firstPayload["profile"])
	assert.EqualValues(t, 1, firstPayload["card_seq"])
	assert.Equal(t, spaceID, loopSpaceIDFromDeepLink(t, firstPayload))
	mention, ok := firstPayload["mention"].(map[string]interface{})
	require.True(t, ok, "reviewer mention must be persisted on the active frame")
	assert.Equal(t, []interface{}{reviewerUID}, mention["uids"], "the explicit reviewer is mentioned exactly once")

	// A worker retry with the same stable wire id must not append a second
	// visible message to the origin group.
	retry := post("/v1/bot/cards/loop/send", firstReq)
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
	var retryResp config.MsgSendResp
	require.NoError(t, json.Unmarshal(retry.Body.Bytes(), &retryResp))
	assert.Equal(t, firstResp.MessageID, retryResp.MessageID, "the API must canonicalize WuKongIM's duplicate-send response to the persisted message")
	assert.Equal(t, 1, countLoopMessagesByClientMsgNo(t, ctx, groupNo, botID, firstClientMsgNo))

	// State changes replace the original frame with a collapsed, action-free
	// superseded card. The frame is edited in place; it is not appended again.
	supersededLoop := map[string]interface{}{
		"variant": "superseded", "issue_id": issueID, "workspace_id": workspaceID,
		"identifier": "LOOP-42", "title": "完成 Web Adaptive Cards",
		"lifecycle_status": "in_review", "priority": "high", "assignee_name": "Octo Agent",
		"revision": 2, "updated_at": "2026-07-15T10:35:00+08:00",
		"superseded_cause": "content_updated",
	}
	edit := post("/v1/bot/cards/loop/edit", map[string]interface{}{
		"message_id": fmt.Sprintf("%d", firstResp.MessageID), "message_seq": firstMessage.MessageSeq,
		"channel_id": groupNo, "channel_type": common.ChannelTypeGroup.Uint8(),
		"card_seq": 2, "loop": supersededLoop,
	})
	require.Equal(t, http.StatusOK, edit.Code, edit.Body.String())

	var editedRaw string
	require.NoError(t, ctx.DB().Select("content_edit").From("message_extra").
		Where("message_id=?", fmt.Sprintf("%d", firstResp.MessageID)).LoadOne(&editedRaw))
	editedPayload := decodeLoopPayload(t, []byte(editedRaw))
	assert.EqualValues(t, 2, editedPayload["card_seq"])
	editedCard, ok := editedPayload["card"].(map[string]interface{})
	require.True(t, ok)
	assert.NotContains(t, editedCard, "actions", "a historical superseded Loop card must be fully non-interactive")
	assert.Contains(t, string([]byte(editedRaw)), "loop.superseded")

	// The replacement active frame is sent after the historical frame is
	// collapsed, so it becomes the newest message in the group conversation.
	nextLoop := map[string]interface{}{
		"variant": "active", "issue_id": issueID, "workspace_id": workspaceID,
		"identifier": "LOOP-42", "title": "完成 Web Adaptive Cards",
		"summary": "已完成真实群聊链路验收。", "lifecycle_status": "in_progress",
		"priority": "high", "assignee_name": "Octo Agent", "progress": "进入最终回归。",
		"revision": 2, "updated_at": "2026-07-15T10:35:00+08:00",
	}
	secondClientMsgNo := fmt.Sprintf("loop:%s:active:2", issueID)
	second := post("/v1/bot/cards/loop/send", map[string]interface{}{
		"channel_id": groupNo, "channel_type": common.ChannelTypeGroup.Uint8(),
		"client_msg_no": secondClientMsgNo, "card_seq": 3, "loop": nextLoop,
	})
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	var secondResp config.MsgSendResp
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &secondResp))
	require.NotZero(t, secondResp.MessageID)
	secondMessage := waitForLoopMessage(t, ctx, groupNo, botID, secondResp.MessageID)
	assert.Greater(t, secondMessage.MessageSeq, firstMessage.MessageSeq, "the replacement Loop card must be the latest group message")

	messages := syncLoopGroupMessages(t, ctx, groupNo, botID)
	require.Len(t, messages, 2, "the lifecycle produces one historical frame and one current frame")
	assert.Equal(t, secondResp.MessageID, messages[len(messages)-1].MessageID)
	assert.Equal(t, secondClientMsgNo, messages[len(messages)-1].ClientMsgNo)
}

func seedLoopCardGroup(t *testing.T, ctx *config.Context, spaceID, groupNo, botID, botToken, reviewerUID, observerUID string) {
	t.Helper()
	_, err := ctx.DB().InsertBySql(
		"INSERT INTO space (space_id,name,description,logo,creator,status) VALUES (?,?,?,?,?,1)",
		spaceID, "Loop E2E Space", "", "", observerUID,
	).Exec()
	require.NoError(t, err)
	_, err = ctx.DB().InsertBySql(
		"INSERT INTO robot (robot_id,status,creator_uid,bot_token) VALUES (?,1,?,?)",
		botID, observerUID, botToken,
	).Exec()
	require.NoError(t, err)
	_, err = ctx.DB().InsertBySql(
		"INSERT INTO `group` (group_no,name,status,version,space_id,creator) VALUES (?,?,1,1,?,?)",
		groupNo, "Loop E2E Group", spaceID, observerUID,
	).Exec()
	require.NoError(t, err)
	for _, uid := range []string{botID, reviewerUID, observerUID} {
		_, err = ctx.DB().InsertBySql(
			"INSERT INTO group_member (group_no,uid,vercode,is_deleted,status,version) VALUES (?,?,?,0,1,1)",
			groupNo, uid, util.GenerUUID(),
		).Exec()
		require.NoError(t, err)
	}
}

func waitForLoopMessage(t *testing.T, ctx *config.Context, groupNo, loginUID string, messageID int64) *config.MessageResp {
	t.Helper()
	for attempt := 0; attempt < 30; attempt++ {
		resp, err := ctx.IMSearchMessages(&config.MsgSearchReq{
			ChannelID: groupNo, ChannelType: common.ChannelTypeGroup.Uint8(),
			MessageIds: []int64{messageID}, LoginUID: loginUID,
		})
		if err == nil && resp != nil && len(resp.Messages) == 1 && resp.Messages[0].MessageSeq > 0 {
			return resp.Messages[0]
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("Loop message %d was not persisted in WuKongIM", messageID)
	return nil
}

func syncLoopGroupMessages(t *testing.T, ctx *config.Context, groupNo, loginUID string) []*config.MessageResp {
	t.Helper()
	for attempt := 0; attempt < 20; attempt++ {
		resp, err := ctx.IMSyncChannelMessage(config.SyncChannelMessageReq{
			LoginUID: loginUID, ChannelID: groupNo, ChannelType: common.ChannelTypeGroup.Uint8(),
			StartMessageSeq: 0, EndMessageSeq: 0, Limit: 100, PullMode: config.PullModeUp,
		})
		if err == nil && resp != nil && len(resp.Messages) >= 2 {
			return resp.Messages
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("Loop group messages were not readable from WuKongIM")
	return nil
}

func countLoopMessagesByClientMsgNo(t *testing.T, ctx *config.Context, groupNo, loginUID, clientMsgNo string) int {
	t.Helper()
	messages := syncLoopGroupMessagesAtLeast(t, ctx, groupNo, loginUID, 1)
	count := 0
	for _, message := range messages {
		if message.ClientMsgNo == clientMsgNo {
			count++
		}
	}
	return count
}

func syncLoopGroupMessagesAtLeast(t *testing.T, ctx *config.Context, groupNo, loginUID string, minimum int) []*config.MessageResp {
	t.Helper()
	for attempt := 0; attempt < 20; attempt++ {
		resp, err := ctx.IMSyncChannelMessage(config.SyncChannelMessageReq{
			LoginUID: loginUID, ChannelID: groupNo, ChannelType: common.ChannelTypeGroup.Uint8(),
			StartMessageSeq: 0, EndMessageSeq: 0, Limit: 100, PullMode: config.PullModeUp,
		})
		if err == nil && resp != nil && len(resp.Messages) >= minimum {
			return resp.Messages
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("expected at least %d Loop group messages", minimum)
	return nil
}

func decodeLoopPayload(t *testing.T, raw []byte) map[string]interface{} {
	t.Helper()
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &payload))
	return payload
}

func loopSpaceIDFromDeepLink(t *testing.T, payload map[string]interface{}) string {
	t.Helper()
	raw, err := json.Marshal(payload["card"])
	require.NoError(t, err)
	var marker struct {
		Metadata struct {
			WebURL string `json:"webUrl"`
		} `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(raw, &marker))
	// A direct URL parse is unnecessary for this assertion; checking the
	// server-authored sp query keeps the helper independent of URL ordering.
	for _, prefix := range []string{"&sp=", "?sp="} {
		if idx := bytes.Index([]byte(marker.Metadata.WebURL), []byte(prefix)); idx >= 0 {
			value := marker.Metadata.WebURL[idx+len(prefix):]
			if end := bytes.IndexByte([]byte(value), '&'); end >= 0 {
				value = value[:end]
			}
			return value
		}
	}
	return ""
}
