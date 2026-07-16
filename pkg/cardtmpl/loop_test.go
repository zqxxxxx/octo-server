package cardtmpl

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Mininglamp-OSS/octo-server/pkg/cardmsg"
	"github.com/Mininglamp-OSS/octo-server/pkg/i18n"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validLoopCard() LoopCard {
	return LoopCard{
		Variant:         LoopCardActive,
		IssueID:         "4fe20b76-7fd5-4ba2-b522-ded8d0f31f33",
		WorkspaceID:     "2224bce3-6c16-418f-9f89-4547c44bc033",
		SpaceID:         "space-1",
		Identifier:      "LOOP-42",
		Title:           "完成 Web Adaptive Cards",
		Summary:         "把 Loop 的关键进展同步回来源群。",
		LifecycleStatus: "in_progress",
		LifecycleLabel:  "进行中",
		PriorityLabel:   "高",
		AssigneeName:    "Octo Agent",
		DueDateLabel:    "2026-07-18",
		Progress:        "已完成协议梳理，正在实现卡片模板。",
		Revision:        3,
		UpdatedAtLabel:  "10 分钟前",
		Source:          Source{Label: "Loop"},
	}
}

func buildLoopEnvelope(t *testing.T, loop LoopCard) map[string]interface{} {
	t.Helper()
	ctx := i18n.WithLanguage(context.Background(), i18n.LanguageDecision{
		Language: "zh-CN",
		Source:   i18n.LanguageSourceTrustedHeader,
	})
	doc, err := BuildLoopCard(ctx, "https://octo.example.com/login", loop)
	require.NoError(t, err)
	var card map[string]interface{}
	require.NoError(t, json.Unmarshal(doc, &card))
	return map[string]interface{}{
		"type":         cardmsg.InteractiveCard.Int(),
		"card_version": cardmsg.CardVersion,
		"profile":      cardmsg.ProfileV2,
		"card":         card,
	}
}

func TestBuildLoopCardActivePassesV2Validation(t *testing.T) {
	envelope := buildLoopEnvelope(t, validLoopCard())
	require.NoError(t, cardmsg.Validate(envelope))

	card := envelope["card"].(map[string]interface{})
	actions := card["actions"].([]interface{})
	require.Len(t, actions, 1)
	assert.Equal(t, "Action.OpenUrl", actions[0].(map[string]interface{})["type"])
	assert.Contains(t, actions[0].(map[string]interface{})["url"], "/loop?")
	assert.Contains(t, actions[0].(map[string]interface{})["url"], "issue=4fe20b76-7fd5-4ba2-b522-ded8d0f31f33")
}

func TestBuildLoopCardPendingConfirmationUsesSubmitAndReviewerGate(t *testing.T) {
	loop := validLoopCard()
	loop.LifecycleStatus = "in_review"
	loop.LifecycleLabel = "待确认"
	loop.Confirmation = &LoopConfirmation{
		ID:          "confirm-1",
		ReviewerUID: "reviewer-uid",
		Prompt:      "请确认这次发布可以上线。",
	}
	envelope := buildLoopEnvelope(t, loop)
	require.NoError(t, cardmsg.Validate(envelope))

	card := envelope["card"].(map[string]interface{})
	actions := card["actions"].([]interface{})
	require.Len(t, actions, 2)
	submit := actions[1].(map[string]interface{})
	assert.Equal(t, "Action.Submit", submit["type"])
	assert.Equal(t, "loop_confirm", submit["id"])
	data := submit["data"].(map[string]interface{})
	assert.Equal(t, "loop.confirm", data["operation"])
	assert.Equal(t, "reviewer-uid", data["reviewer_uid"])

	metadata := card["metadata"].(map[string]interface{})
	octo := metadata["octo"].(map[string]interface{})
	loopMeta := octo["loop"].(map[string]interface{})
	assert.Equal(t, "reviewer-uid", loopMeta["reviewerUid"])
}

func TestBuildLoopCardConfirmationResultIsVisibleAndActionFree(t *testing.T) {
	loop := validLoopCard()
	loop.ConfirmationResultLabel = "已确认"
	envelope := buildLoopEnvelope(t, loop)
	require.NoError(t, cardmsg.Validate(envelope))
	card := envelope["card"].(map[string]interface{})
	raw, err := json.Marshal(card["body"])
	require.NoError(t, err)
	assert.Contains(t, string(raw), "确认结果")
	assert.Contains(t, string(raw), "已确认")
	actions := card["actions"].([]interface{})
	assert.Len(t, actions, 1)
}

func TestBuildLoopCardSupersededIsCollapsedAndActionFree(t *testing.T) {
	loop := validLoopCard()
	loop.Variant = LoopCardSuperseded
	loop.SupersededReason = "负责人已变更，新卡片已发送。"
	envelope := buildLoopEnvelope(t, loop)
	require.NoError(t, cardmsg.Validate(envelope))

	card := envelope["card"].(map[string]interface{})
	_, hasActions := card["actions"]
	assert.False(t, hasActions)
	raw, err := json.Marshal(card["body"])
	require.NoError(t, err)
	assert.Contains(t, string(raw), "已失效")
	assert.NotContains(t, string(raw), loop.Progress)
	assert.NotContains(t, string(raw), `"color":"Attention"`)
}

func TestBuildLoopCardDeletedHasNoActions(t *testing.T) {
	loop := validLoopCard()
	loop.Variant = LoopCardDeleted
	loop.Title = ""
	loop.LifecycleStatus = "deleted"
	loop.LifecycleLabel = "已删除"
	envelope := buildLoopEnvelope(t, loop)
	require.NoError(t, cardmsg.Validate(envelope))
	card := envelope["card"].(map[string]interface{})
	_, hasActions := card["actions"]
	assert.False(t, hasActions)
}

func TestBuildLoopCardEscapesUntrustedMarkdown(t *testing.T) {
	loop := validLoopCard()
	loop.Title = "[伪造](javascript:alert(1))"
	envelope := buildLoopEnvelope(t, loop)
	require.NoError(t, cardmsg.Validate(envelope))
	raw, err := json.Marshal(envelope["card"])
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "[伪造](javascript:")
	assert.Contains(t, string(raw), `\\[伪造\\]\\(javascript:alert\\(1\\)\\)`)
}

func TestBuildLoopCardRejectsInvalidConfirmationAndSupersededState(t *testing.T) {
	loop := validLoopCard()
	loop.Confirmation = &LoopConfirmation{ID: "confirm-1"}
	_, err := BuildLoopCard(context.Background(), "https://octo.example.com/login", loop)
	assert.ErrorContains(t, err, "reviewer UID")

	loop = validLoopCard()
	loop.Variant = LoopCardSuperseded
	_, err = BuildLoopCard(context.Background(), "https://octo.example.com/login", loop)
	assert.ErrorContains(t, err, "superseded loop reason")
}

func TestBuildLoopCardRejectsNonHTTPSBase(t *testing.T) {
	_, err := BuildLoopCard(context.Background(), "http://octo.example.com", validLoopCard())
	assert.ErrorContains(t, err, "absolute https")
}
