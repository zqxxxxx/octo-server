package bot_api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-lib/common"
	"github.com/Mininglamp-OSS/octo-lib/pkg/wkhttp"
	"github.com/Mininglamp-OSS/octo-server/pkg/cardmsg"
	"github.com/Mininglamp-OSS/octo-server/pkg/cardtmpl"
	"github.com/Mininglamp-OSS/octo-server/pkg/i18n"
	"github.com/gocraft/dbr/v2"
	"go.uber.org/zap"
)

type botLoopCardSendReq struct {
	ChannelID       string          `json:"channel_id"`
	ChannelType     uint8           `json:"channel_type"`
	ClientMsgNo     string          `json:"client_msg_no"`
	MentionReviewer bool            `json:"mention_reviewer"`
	CardSeq         int64           `json:"card_seq"`
	Loop            botLoopCardData `json:"loop"`
}

type botLoopCardEditReq struct {
	MessageID   string          `json:"message_id"`
	MessageSeq  uint32          `json:"message_seq"`
	ChannelID   string          `json:"channel_id"`
	ChannelType uint8           `json:"channel_type"`
	CardSeq     int64           `json:"card_seq"`
	Loop        botLoopCardData `json:"loop"`
}

// botLoopCardData is intentionally raw and non-localized. Fields such as
// Space, source, lifecycle labels, priority labels, timestamps, and
// superseded copy are derived by octo-server.
type botLoopCardData struct {
	Variant            cardtmpl.LoopCardVariant   `json:"variant"`
	IssueID            string                     `json:"issue_id"`
	WorkspaceID        string                     `json:"workspace_id"`
	Identifier         string                     `json:"identifier"`
	Title              string                     `json:"title"`
	Summary            string                     `json:"summary"`
	LifecycleStatus    string                     `json:"lifecycle_status"`
	Priority           string                     `json:"priority"`
	AssigneeName       string                     `json:"assignee_name"`
	DueDate            string                     `json:"due_date"`
	Progress           string                     `json:"progress"`
	ConfirmationStatus string                     `json:"confirmation_status"`
	Revision           int64                      `json:"revision"`
	UpdatedAt          string                     `json:"updated_at"`
	SupersededCause    string                     `json:"superseded_cause"`
	Confirmation       *cardtmpl.LoopConfirmation `json:"confirmation,omitempty"`
}

// botLoopCardSend accepts structured Loop display data, derives the Space from
// the authenticated target, and then enters the same bot send gate as every
// other message. Caller-authored type-17 JSON is never accepted here.
func (ba *BotAPI) botLoopCardSend(c *wkhttp.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, cardmsg.MaxSendBodyBytes)
	var req botLoopCardSendReq
	if err := c.BindJSON(&req); err != nil {
		respondBotAPIRequestInvalid(c, "")
		return
	}
	if req.ClientMsgNo == "" || req.ClientMsgNo != strings.TrimSpace(req.ClientMsgNo) || len(req.ClientMsgNo) > 64 {
		respondBotAPIRequestInvalid(c, "client_msg_no")
		return
	}
	payload, err := ba.buildLoopCardPayload(c, req.ChannelID, req.ChannelType, req.CardSeq, req.Loop)
	if err != nil {
		ba.Warn("build Loop card failed", zap.Error(err))
		respondBotAPIRequestInvalid(c, "loop")
		return
	}
	if err := addLoopReviewerMention(payload, req.Loop, req.MentionReviewer); err != nil {
		respondBotAPIRequestInvalid(c, "mention_reviewer")
		return
	}
	ba.sendMessageRequest(c, BotSendMessageReq{
		ChannelID: req.ChannelID, ChannelType: req.ChannelType, ClientMsgNo: req.ClientMsgNo, Payload: payload,
	})
}

func addLoopReviewerMention(payload map[string]interface{}, loop botLoopCardData, enabled bool) error {
	if !enabled {
		return nil
	}
	if loop.Confirmation == nil || strings.TrimSpace(loop.Confirmation.ReviewerUID) == "" {
		return errors.New("reviewer mention requires a confirmation reviewer")
	}
	payload["mention"] = map[string]interface{}{"uids": []string{loop.Confirmation.ReviewerUID}}
	return nil
}

// botLoopCardEdit performs a full-frame replacement through the existing
// ownership and card_seq CAS path.
func (ba *BotAPI) botLoopCardEdit(c *wkhttp.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, cardmsg.MaxSendBodyBytes)
	var req botLoopCardEditReq
	if err := c.BindJSON(&req); err != nil {
		respondBotAPIRequestInvalid(c, "")
		return
	}
	payload, err := ba.buildLoopCardPayload(c, req.ChannelID, req.ChannelType, req.CardSeq, req.Loop)
	if err != nil {
		ba.Warn("build Loop card edit failed", zap.Error(err))
		respondBotAPIRequestInvalid(c, "loop")
		return
	}
	contentEdit, err := json.Marshal(payload)
	if err != nil {
		respondBotAPIRequestInvalid(c, "loop")
		return
	}
	ba.botMessageEditRequest(c, BotMessageEditReq{
		MessageID: req.MessageID, MessageSeq: req.MessageSeq,
		ChannelID: req.ChannelID, ChannelType: req.ChannelType,
		ContentEdit: string(contentEdit),
	})
}

func (ba *BotAPI) buildLoopCardPayload(c *wkhttp.Context, channelID string, channelType uint8, cardSeq int64, input botLoopCardData) (map[string]interface{}, error) {
	if c == nil || c.Request == nil || ba.ctx == nil || ba.ctx.GetConfig() == nil {
		return nil, errors.New("Loop card context is unavailable")
	}
	if strings.TrimSpace(channelID) == "" || cardSeq < 1 {
		return nil, errors.New("channel and positive card_seq are required")
	}
	spaceID, err := ba.resolveLoopCardSpaceID(channelID, channelType)
	if err != nil {
		return nil, err
	}
	loop, err := input.toTemplate(c.Request.Context(), spaceID)
	if err != nil {
		return nil, err
	}
	return makeLoopCardPayload(c.Request.Context(), ba.ctx.GetConfig().External.WebLoginURL, cardSeq, loop)
}

func (input botLoopCardData) toTemplate(ctx context.Context, spaceID string) (cardtmpl.LoopCard, error) {
	priority, err := localizedLoopPriority(i18n.OutboundLanguage(ctx), input.Priority)
	if err != nil {
		return cardtmpl.LoopCard{}, err
	}
	dueDate, err := normalizedLoopDate(input.DueDate)
	if err != nil {
		return cardtmpl.LoopCard{}, err
	}
	updatedAt, err := normalizedLoopTimestamp(input.UpdatedAt)
	if err != nil {
		return cardtmpl.LoopCard{}, err
	}
	reason, err := localizedSupersededReason(i18n.OutboundLanguage(ctx), input.SupersededCause, input.Variant)
	if err != nil {
		return cardtmpl.LoopCard{}, err
	}
	confirmationResult, err := localizedLoopConfirmationStatus(i18n.OutboundLanguage(ctx), input.ConfirmationStatus)
	if err != nil {
		return cardtmpl.LoopCard{}, err
	}
	return cardtmpl.LoopCard{
		Variant: input.Variant, IssueID: input.IssueID, WorkspaceID: input.WorkspaceID, SpaceID: spaceID,
		Identifier: input.Identifier, Title: input.Title, Summary: input.Summary,
		LifecycleStatus: input.LifecycleStatus, PriorityLabel: priority, AssigneeName: input.AssigneeName,
		DueDateLabel: dueDate, Progress: input.Progress, Revision: input.Revision,
		UpdatedAtLabel: updatedAt, SupersededReason: reason, ConfirmationResultLabel: confirmationResult,
		Source: cardtmpl.Source{Label: "Loop"}, Confirmation: input.Confirmation,
	}, nil
}

func localizedLoopConfirmationStatus(language, status string) (string, error) {
	zh := strings.HasPrefix(strings.ToLower(language), "zh-")
	labels := map[string][2]string{
		"": {"", ""}, "confirmed": {"已确认", "Confirmed"},
		"rejected": {"已拒绝", "Rejected"}, "cancelled": {"已取消", "Cancelled"},
	}
	pair, ok := labels[status]
	if !ok {
		return "", errors.New("invalid Loop confirmation_status")
	}
	if zh {
		return pair[0], nil
	}
	return pair[1], nil
}

func localizedLoopPriority(language, priority string) (string, error) {
	zh := strings.HasPrefix(strings.ToLower(language), "zh-")
	labels := map[string][2]string{
		"": {"", ""}, "none": {"", ""}, "low": {"低", "Low"}, "medium": {"中", "Medium"},
		"high": {"高", "High"}, "urgent": {"紧急", "Urgent"},
	}
	pair, ok := labels[priority]
	if !ok {
		return "", errors.New("invalid Loop priority")
	}
	if zh {
		return pair[0], nil
	}
	return pair[1], nil
}

func normalizedLoopDate(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	parsed, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return "", errors.New("invalid Loop due_date")
	}
	return parsed.Format("2006-01-02"), nil
}

func normalizedLoopTimestamp(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return "", errors.New("invalid Loop updated_at")
	}
	return parsed.Format("2006-01-02 15:04 MST"), nil
}

func localizedSupersededReason(language, cause string, variant cardtmpl.LoopCardVariant) (string, error) {
	if variant != cardtmpl.LoopCardSuperseded {
		return "", nil
	}
	zh := strings.HasPrefix(strings.ToLower(language), "zh-")
	reasons := map[string][2]string{
		"content_updated":               {"Loop 内容或进度已更新，请查看下方新卡片。", "Loop content or progress changed. Use the newer card below."},
		"status_changed":                {"Loop 状态已变更，请查看下方新卡片。", "Loop status changed. Use the newer card below."},
		"assignee_changed":              {"Loop 负责人已变更，请查看下方新卡片。", "Loop assignee changed. Use the newer card below."},
		"due_date_changed":              {"Loop 截止时间已变更，请查看下方新卡片。", "Loop due date changed. Use the newer card below."},
		"confirmation_requested":        {"Loop 已进入确认阶段，请查看下方新卡片。", "Loop confirmation is pending. Use the newer card below."},
		"confirmation_reviewer_changed": {"Loop 审核人已变更，请查看下方新卡片。", "The Loop reviewer changed. Use the newer card below."},
		"confirmation_resolved":         {"Loop 确认结果已更新，请查看下方新卡片。", "Loop confirmation changed. Use the newer card below."},
		"confirmation_cancelled":        {"Loop 确认已取消，请查看下方新卡片。", "Loop confirmation was cancelled. Use the newer card below."},
		"deleted":                       {"Loop 已删除，请查看下方结果卡片。", "The Loop was deleted. See the result card below."},
	}
	pair, ok := reasons[cause]
	if !ok {
		return "", errors.New("invalid Loop superseded_cause")
	}
	if zh {
		return pair[0], nil
	}
	return pair[1], nil
}

func makeLoopCardPayload(ctx context.Context, webLoginURL string, cardSeq int64, loop cardtmpl.LoopCard) (map[string]interface{}, error) {
	if cardSeq < 1 {
		return nil, errors.New("positive card_seq is required")
	}
	document, err := cardtmpl.BuildLoopCard(ctx, webLoginURL, loop)
	if err != nil {
		return nil, err
	}
	var card map[string]interface{}
	if err := json.Unmarshal(document, &card); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"type":         cardmsg.InteractiveCard.Int(),
		"card_version": cardmsg.CardVersion,
		"profile":      cardmsg.ProfileV2,
		"card_seq":     cardSeq,
		"card":         card,
	}, nil
}

func (ba *BotAPI) resolveLoopCardSpaceID(channelID string, channelType uint8) (string, error) {
	groupNo := channelID
	if channelType == common.ChannelTypeCommunityTopic.Uint8() {
		parts := strings.SplitN(channelID, threadChannelIDSeparator, 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", errors.New("invalid thread channel")
		}
		groupNo = parts[0]
	} else if channelType != common.ChannelTypeGroup.Uint8() {
		return "", errors.New("Loop cards require a group or thread origin")
	}
	if ba.db == nil || ba.db.session == nil {
		return "", errors.New("Loop card Space resolver is unavailable")
	}
	var row struct {
		SpaceID string `db:"space_id"`
		Status  int    `db:"status"`
	}
	err := ba.db.session.SelectBySql("SELECT space_id,status FROM `group` WHERE group_no=?", groupNo).LoadOne(&row)
	if err != nil {
		if err == dbr.ErrNotFound {
			return "", errors.New("origin group not found")
		}
		return "", err
	}
	if row.Status != 1 || strings.TrimSpace(row.SpaceID) == "" {
		return "", errors.New("origin group is not active in a Space")
	}
	return row.SpaceID, nil
}
