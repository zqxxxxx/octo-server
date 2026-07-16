package bot_api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Mininglamp-OSS/octo-server/pkg/cardmsg"
	"github.com/Mininglamp-OSS/octo-server/pkg/cardtmpl"
	"github.com/Mininglamp-OSS/octo-server/pkg/i18n"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validStructuredLoopCard() cardtmpl.LoopCard {
	return cardtmpl.LoopCard{
		Variant:         cardtmpl.LoopCardActive,
		IssueID:         "4fe20b76-7fd5-4ba2-b522-ded8d0f31f33",
		WorkspaceID:     "2224bce3-6c16-418f-9f89-4547c44bc033",
		SpaceID:         "space-authoritative",
		Identifier:      "LOOP-42",
		Title:           "完成 Web Adaptive Cards",
		Summary:         "把 Loop 的关键进展同步回来源群。",
		LifecycleStatus: "in_progress",
		LifecycleLabel:  "caller-forged-label",
		PriorityLabel:   "高",
		AssigneeName:    "Octo Agent",
		Revision:        3,
	}
}

func TestMakeLoopCardPayloadBuildsValidatedV2Envelope(t *testing.T) {
	ctx := i18n.WithLanguage(context.Background(), i18n.LanguageDecision{
		Language: "zh-CN", Source: i18n.LanguageSourceTrustedHeader,
	})
	payload, err := makeLoopCardPayload(ctx, "https://octo.example.com/login", 7, validStructuredLoopCard())
	require.NoError(t, err)
	require.NoError(t, cardmsg.Validate(payload))
	assert.Equal(t, cardmsg.ProfileV2, payload["profile"])
	assert.Equal(t, int64(7), payload["card_seq"])

	raw, err := json.Marshal(payload["card"])
	require.NoError(t, err)
	assert.Contains(t, string(raw), "进行中")
	assert.NotContains(t, string(raw), "caller-forged-label")
	assert.Contains(t, string(raw), "sp=space-authoritative")
}

func TestStructuredLoopRequestCannotSupplySpaceOrSource(t *testing.T) {
	var req botLoopCardSendReq
	err := json.Unmarshal([]byte(`{
		"channel_id":"group-1","channel_type":2,"card_seq":1,
		"loop":{"issue_id":"issue-1","space_id":"space-forged","source":{"label":"forged"}}
	}`), &req)
	require.NoError(t, err)
	assert.Equal(t, "issue-1", req.Loop.IssueID)
	raw, err := json.Marshal(req.Loop)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "space-forged")
	assert.NotContains(t, string(raw), "forged")
}

func TestMakeLoopCardPayloadRejectsInvalidCardSeq(t *testing.T) {
	_, err := makeLoopCardPayload(context.Background(), "https://octo.example.com", 0, validStructuredLoopCard())
	assert.ErrorContains(t, err, "positive card_seq")
}

func TestLoopReviewerMentionIsDerivedFromConfirmation(t *testing.T) {
	payload := map[string]interface{}{"type": cardmsg.InteractiveCard.Int()}
	loop := botLoopCardData{Confirmation: &cardtmpl.LoopConfirmation{ID: "confirmation-1", ReviewerUID: "reviewer-uid"}}
	require.NoError(t, addLoopReviewerMention(payload, loop, true))
	mention, ok := payload["mention"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, []string{"reviewer-uid"}, mention["uids"])

	payload = map[string]interface{}{}
	require.NoError(t, addLoopReviewerMention(payload, botLoopCardData{}, false))
	assert.NotContains(t, payload, "mention")
	assert.Error(t, addLoopReviewerMention(payload, botLoopCardData{}, true))
}

func TestStructuredLoopConfirmationStatusIsServerLocalized(t *testing.T) {
	ctx := i18n.WithLanguage(context.Background(), i18n.LanguageDecision{
		Language: "zh-CN", Source: i18n.LanguageSourceTrustedHeader,
	})
	loop, err := (botLoopCardData{
		Variant: cardtmpl.LoopCardActive, IssueID: "issue-1", WorkspaceID: "workspace-1",
		Identifier: "LOOP-1", Title: "Ship", LifecycleStatus: "in_review",
		ConfirmationStatus: "confirmed", Revision: 4,
	}).toTemplate(ctx, "space-authoritative")
	require.NoError(t, err)
	assert.Equal(t, "已确认", loop.ConfirmationResultLabel)
}

func TestStructuredLoopDataLocalizesServerOwnedLabels(t *testing.T) {
	ctx := i18n.WithLanguage(context.Background(), i18n.LanguageDecision{
		Language: "zh-CN", Source: i18n.LanguageSourceTrustedHeader,
	})
	loop, err := (botLoopCardData{
		Variant: cardtmpl.LoopCardSuperseded, IssueID: "issue-1", WorkspaceID: "workspace-1",
		Identifier: "LOOP-1", Title: "Ship", LifecycleStatus: "todo", Priority: "urgent",
		DueDate: "2026-07-20", UpdatedAt: "2026-07-15T10:30:00+08:00", Revision: 4,
		SupersededCause: "assignee_changed",
	}).toTemplate(ctx, "space-authoritative")
	require.NoError(t, err)
	assert.Equal(t, "紧急", loop.PriorityLabel)
	assert.Equal(t, "Loop 负责人已变更，请查看下方新卡片。", loop.SupersededReason)
	assert.Equal(t, "space-authoritative", loop.SpaceID)
	assert.Equal(t, "Loop", loop.Source.Label)
}

func TestStructuredLoopDataAcceptsContentUpdatedSupersededCause(t *testing.T) {
	ctx := i18n.WithLanguage(context.Background(), i18n.LanguageDecision{
		Language: "zh-CN", Source: i18n.LanguageSourceTrustedHeader,
	})
	loop, err := (botLoopCardData{
		Variant: cardtmpl.LoopCardSuperseded, IssueID: "issue-1", WorkspaceID: "workspace-1",
		Identifier: "LOOP-1", Title: "Ship", LifecycleStatus: "in_progress", Revision: 5,
		SupersededCause: "content_updated",
	}).toTemplate(ctx, "space-authoritative")
	require.NoError(t, err)
	assert.Equal(t, "Loop 内容或进度已更新，请查看下方新卡片。", loop.SupersededReason)
}
