package cardtmpl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-server/pkg/cardmsg"
	"github.com/Mininglamp-OSS/octo-server/pkg/i18n"
)

const (
	maxLoopSummaryRunes  = 500
	maxLoopProgressRunes = 500
	maxLoopLabelRunes    = 100
)

// LoopCardVariant describes the immutable presentation role of one Loop card
// frame. A superseded frame is intentionally collapsed and action-free.
type LoopCardVariant string

const (
	LoopCardActive     LoopCardVariant = "active"
	LoopCardSuperseded LoopCardVariant = "superseded"
	LoopCardFinal      LoopCardVariant = "final"
	LoopCardDeleted    LoopCardVariant = "deleted"
)

// LoopConfirmation contains the server-authoritative pending confirmation.
// ReviewerUID is consumed by Octo Web to hide the quick action from other
// viewers; the action endpoint must still enforce the same UID independently.
type LoopConfirmation struct {
	ID          string `json:"id"`
	ReviewerUID string `json:"reviewer_uid"`
	Prompt      string `json:"prompt"`
}

// LoopCard is the structured input accepted from the Loop domain. It contains
// display data only; credentials and bearer tokens must never enter this type.
type LoopCard struct {
	Variant                 LoopCardVariant   `json:"variant"`
	IssueID                 string            `json:"issue_id"`
	WorkspaceID             string            `json:"workspace_id"`
	SpaceID                 string            `json:"-"`
	Identifier              string            `json:"identifier"`
	Title                   string            `json:"title"`
	Summary                 string            `json:"summary"`
	LifecycleStatus         string            `json:"lifecycle_status"`
	LifecycleLabel          string            `json:"lifecycle_label"`
	PriorityLabel           string            `json:"priority_label"`
	AssigneeName            string            `json:"assignee_name"`
	DueDateLabel            string            `json:"due_date_label"`
	Progress                string            `json:"progress"`
	ConfirmationResultLabel string            `json:"-"`
	Revision                int64             `json:"revision"`
	UpdatedAtLabel          string            `json:"updated_at_label"`
	SupersededReason        string            `json:"superseded_reason"`
	Source                  Source            `json:"-"`
	Confirmation            *LoopConfirmation `json:"confirmation,omitempty"`
}

// BuildLoopCard builds an Adaptive Card 1.5 document for an origin-bound Loop.
// The caller wraps the document in a type=17 octo/v2 message envelope.
func BuildLoopCard(ctx context.Context, webLoginURL string, loop LoopCard) (json.RawMessage, error) {
	labels := loopLabelsForLanguage(i18n.OutboundLanguage(ctx))
	// Domain callers send the raw lifecycle status. The server owns the
	// localized label so one producer cannot introduce divergent card copy.
	loop.LifecycleLabel = labels.lifecycleLabel(loop.LifecycleStatus)
	if err := validateLoopCard(loop); err != nil {
		return nil, err
	}
	deepLink, err := loopDeepLink(webLoginURL, loop.IssueID, loop.WorkspaceID, loop.SpaceID)
	if err != nil {
		return nil, err
	}

	metadata := buildLoopMetadata(deepLink, loop)
	body := buildLoopBody(loop, labels)
	actions := buildLoopActions(deepLink, loop, labels)

	document := map[string]interface{}{
		"type":     "AdaptiveCard",
		"version":  cardmsg.CardVersion,
		"metadata": metadata,
		"body":     body,
	}
	if len(actions) > 0 {
		document["actions"] = actions
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("cardtmpl: marshal loop card: %w", err)
	}
	return json.RawMessage(raw), nil
}

func validateLoopCard(loop LoopCard) error {
	if loop.Variant != LoopCardActive && loop.Variant != LoopCardSuperseded &&
		loop.Variant != LoopCardFinal && loop.Variant != LoopCardDeleted {
		return errors.New("cardtmpl: invalid loop card variant")
	}
	if strings.TrimSpace(loop.IssueID) == "" || strings.TrimSpace(loop.WorkspaceID) == "" ||
		strings.TrimSpace(loop.SpaceID) == "" {
		return errors.New("cardtmpl: loop issue, workspace, and space IDs are required")
	}
	if strings.TrimSpace(loop.Identifier) == "" || utf8.RuneCountInString(loop.Identifier) > maxLoopLabelRunes {
		return errors.New("cardtmpl: invalid loop identifier")
	}
	if loop.Variant != LoopCardDeleted &&
		(strings.TrimSpace(loop.Title) == "" || utf8.RuneCountInString(loop.Title) > maxTitleRunes) {
		return errors.New("cardtmpl: invalid loop title")
	}
	if strings.TrimSpace(loop.LifecycleStatus) == "" || utf8.RuneCountInString(loop.LifecycleStatus) > maxLoopLabelRunes ||
		strings.TrimSpace(loop.LifecycleLabel) == "" || utf8.RuneCountInString(loop.LifecycleLabel) > maxLoopLabelRunes {
		return errors.New("cardtmpl: invalid loop lifecycle")
	}
	if loop.Revision < 1 {
		return errors.New("cardtmpl: loop revision must be positive")
	}
	for _, value := range []string{loop.PriorityLabel, loop.AssigneeName, loop.DueDateLabel, loop.UpdatedAtLabel, loop.SupersededReason, loop.ConfirmationResultLabel} {
		if utf8.RuneCountInString(value) > maxTitleRunes {
			return errors.New("cardtmpl: loop label is too long")
		}
	}
	if utf8.RuneCountInString(loop.Summary) > maxLoopSummaryRunes || utf8.RuneCountInString(loop.Progress) > maxLoopProgressRunes {
		return errors.New("cardtmpl: loop text is too long")
	}
	if loop.Variant == LoopCardSuperseded && strings.TrimSpace(loop.SupersededReason) == "" {
		return errors.New("cardtmpl: superseded loop reason is required")
	}
	if loop.Confirmation != nil {
		if strings.TrimSpace(loop.ConfirmationResultLabel) != "" {
			return errors.New("cardtmpl: pending confirmation and confirmation result are mutually exclusive")
		}
		if loop.Variant != LoopCardActive {
			return errors.New("cardtmpl: confirmation is allowed only on an active loop card")
		}
		if strings.TrimSpace(loop.Confirmation.ID) == "" || strings.TrimSpace(loop.Confirmation.ReviewerUID) == "" {
			return errors.New("cardtmpl: confirmation ID and reviewer UID are required")
		}
		if utf8.RuneCountInString(loop.Confirmation.Prompt) > maxLoopProgressRunes {
			return errors.New("cardtmpl: confirmation prompt is too long")
		}
	}
	if loop.Source.IconURL != "" {
		if err := requireHTTPS(loop.Source.IconURL); err != nil {
			return fmt.Errorf("cardtmpl: source icon URL: %w", err)
		}
	}
	return nil
}

func buildLoopBody(loop LoopCard, labels loopLocalizedLabels) []interface{} {
	headerFacts := []interface{}{
		map[string]interface{}{"title": labels.status, "value": escapeMarkdown(loop.LifecycleLabel)},
	}
	if strings.TrimSpace(loop.PriorityLabel) != "" {
		headerFacts = append(headerFacts, map[string]interface{}{"title": labels.priority, "value": escapeMarkdown(loop.PriorityLabel)})
	}

	if loop.Variant == LoopCardSuperseded {
		return []interface{}{
			map[string]interface{}{
				"type": "ColumnSet",
				"columns": []interface{}{
					map[string]interface{}{"type": "Column", "width": "stretch", "items": []interface{}{
						map[string]interface{}{"type": "TextBlock", "text": "Loop · " + escapeMarkdown(loop.Identifier), "weight": "Bolder", "wrap": true},
					}},
					map[string]interface{}{"type": "Column", "width": "auto", "items": []interface{}{
						map[string]interface{}{"type": "TextBlock", "text": labels.expired, "isSubtle": true, "size": "Small", "weight": "Bolder"},
					}},
				},
			},
			map[string]interface{}{"type": "TextBlock", "text": escapeMarkdown(loop.Title), "weight": "Bolder", "wrap": true, "spacing": "Small"},
			map[string]interface{}{"type": "TextBlock", "text": escapeMarkdown(loop.SupersededReason), "isSubtle": true, "wrap": true, "spacing": "Small"},
			map[string]interface{}{"type": "TextBlock", "text": escapeMarkdown(labels.expiredHint(loop.UpdatedAtLabel)), "isSubtle": true, "wrap": true, "spacing": "Small"},
		}
	}

	body := []interface{}{
		map[string]interface{}{
			"type": "ColumnSet",
			"columns": []interface{}{
				map[string]interface{}{"type": "Column", "width": "stretch", "items": []interface{}{
					map[string]interface{}{"type": "TextBlock", "text": "Loop · " + escapeMarkdown(loop.Identifier), "weight": "Bolder", "wrap": true},
				}},
				map[string]interface{}{"type": "Column", "width": "auto", "items": []interface{}{
					map[string]interface{}{"type": "TextBlock", "text": escapeMarkdown(loop.LifecycleLabel), "weight": "Bolder", "color": lifecycleColor(loop.LifecycleStatus)},
				}},
			},
		},
	}
	if loop.Variant == LoopCardDeleted {
		return append(body,
			map[string]interface{}{"type": "TextBlock", "text": labels.deleted, "weight": "Bolder", "wrap": true, "spacing": "Medium"},
			map[string]interface{}{"type": "TextBlock", "text": labels.deletedHint, "isSubtle": true, "wrap": true, "spacing": "Small"},
		)
	}

	body = append(body,
		map[string]interface{}{"type": "TextBlock", "text": escapeMarkdown(loop.Title), "weight": "Bolder", "size": "Medium", "wrap": true, "spacing": "Medium"},
	)
	if strings.TrimSpace(loop.Summary) != "" {
		body = append(body, map[string]interface{}{"type": "TextBlock", "text": escapeMarkdown(loop.Summary), "wrap": true, "spacing": "Small"})
	}
	body = append(body, map[string]interface{}{"type": "FactSet", "facts": headerFacts})

	detailFacts := make([]interface{}, 0, 2)
	if strings.TrimSpace(loop.AssigneeName) != "" {
		detailFacts = append(detailFacts, map[string]interface{}{"title": labels.assignee, "value": escapeMarkdown(loop.AssigneeName)})
	}
	if strings.TrimSpace(loop.DueDateLabel) != "" {
		detailFacts = append(detailFacts, map[string]interface{}{"title": labels.dueDate, "value": escapeMarkdown(loop.DueDateLabel)})
	}
	if len(detailFacts) > 0 {
		body = append(body, map[string]interface{}{"type": "FactSet", "facts": detailFacts, "spacing": "Small"})
	}
	if strings.TrimSpace(loop.Progress) != "" {
		body = append(body, map[string]interface{}{
			"type":  "Container",
			"style": "emphasis",
			"items": []interface{}{
				map[string]interface{}{"type": "TextBlock", "text": labels.latestProgress, "weight": "Bolder", "size": "Small"},
				map[string]interface{}{"type": "TextBlock", "text": escapeMarkdown(loop.Progress), "wrap": true, "spacing": "Small"},
			},
			"spacing": "Medium",
		})
	}
	if loop.Confirmation != nil {
		prompt := strings.TrimSpace(loop.Confirmation.Prompt)
		if prompt == "" {
			prompt = labels.confirmationPending
		}
		body = append(body, map[string]interface{}{
			"type":  "Container",
			"style": "attention",
			"items": []interface{}{
				map[string]interface{}{"type": "TextBlock", "text": labels.confirmationPending, "weight": "Bolder", "wrap": true},
				map[string]interface{}{"type": "TextBlock", "text": escapeMarkdown(prompt), "wrap": true, "spacing": "Small"},
			},
			"spacing": "Medium",
		})
	}
	if strings.TrimSpace(loop.ConfirmationResultLabel) != "" {
		body = append(body, map[string]interface{}{
			"type": "Container", "style": "emphasis", "spacing": "Medium",
			"items": []interface{}{
				map[string]interface{}{"type": "TextBlock", "text": labels.confirmationResult, "weight": "Bolder", "size": "Small"},
				map[string]interface{}{"type": "TextBlock", "text": escapeMarkdown(loop.ConfirmationResultLabel), "wrap": true, "spacing": "Small"},
			},
		})
	}
	footer := labels.revision + " " + strconv.FormatInt(loop.Revision, 10)
	if strings.TrimSpace(loop.UpdatedAtLabel) != "" {
		footer += " · " + loop.UpdatedAtLabel
	}
	body = append(body, map[string]interface{}{"type": "TextBlock", "text": escapeMarkdown(footer), "isSubtle": true, "size": "Small", "spacing": "Medium", "wrap": true})
	return body
}

func buildLoopActions(deepLink string, loop LoopCard, labels loopLocalizedLabels) []interface{} {
	if loop.Variant == LoopCardSuperseded || loop.Variant == LoopCardDeleted {
		return nil
	}
	actions := []interface{}{
		map[string]interface{}{"type": "Action.OpenUrl", "title": labels.viewLoop, "url": deepLink},
	}
	if loop.Confirmation != nil {
		actions = append(actions, map[string]interface{}{
			"type":  "Action.Submit",
			"id":    "loop_confirm",
			"title": labels.quickConfirm,
			"data": map[string]interface{}{
				"operation":       "loop.confirm",
				"issue_id":        loop.IssueID,
				"confirmation_id": loop.Confirmation.ID,
				"loop_revision":   loop.Revision,
				"reviewer_uid":    loop.Confirmation.ReviewerUID,
			},
		})
	}
	return actions
}

func buildLoopMetadata(deepLink string, loop LoopCard) map[string]interface{} {
	metadata := buildMetadata(deepLink, "loop."+string(loop.Variant), loop.Source)
	octo, _ := metadata["octo"].(map[string]interface{})
	if octo == nil {
		octo = map[string]interface{}{}
		metadata["octo"] = octo
	}
	loopMetadata := map[string]interface{}{
		"issueId":         loop.IssueID,
		"workspaceId":     loop.WorkspaceID,
		"identifier":      loop.Identifier,
		"lifecycleStatus": loop.LifecycleStatus,
		"revision":        loop.Revision,
		"frame":           string(loop.Variant),
	}
	if loop.Confirmation != nil {
		loopMetadata["reviewerUid"] = loop.Confirmation.ReviewerUID
		loopMetadata["confirmationId"] = loop.Confirmation.ID
	}
	octo["loop"] = loopMetadata
	return metadata
}

func loopDeepLink(webLoginURL, issueID, workspaceID, spaceID string) (string, error) {
	origin, err := webOrigin(webLoginURL)
	if err != nil {
		return "", err
	}
	query := url.Values{}
	query.Set("issue", issueID)
	query.Set("workspace", workspaceID)
	query.Set("sp", spaceID)
	return origin + "/loop?" + query.Encode(), nil
}

func lifecycleColor(status string) string {
	switch status {
	case "done":
		return "Good"
	case "blocked", "cancelled":
		return "Attention"
	case "in_progress", "in_review":
		return "Accent"
	default:
		return "Default"
	}
}

type loopLocalizedLabels struct {
	status              string
	priority            string
	assignee            string
	dueDate             string
	latestProgress      string
	revision            string
	viewLoop            string
	quickConfirm        string
	confirmationPending string
	confirmationResult  string
	expired             string
	deleted             string
	deletedHint         string
	expiredHint         func(string) string
	lifecycleLabel      func(string) string
}

func loopLabelsForLanguage(lang string) loopLocalizedLabels {
	if strings.EqualFold(lang, "zh-CN") || strings.HasPrefix(strings.ToLower(lang), "zh-") {
		return loopLocalizedLabels{
			status: "状态", priority: "优先级", assignee: "负责人", dueDate: "截止时间",
			latestProgress: "最新进展", revision: "版本", viewLoop: "查看 Loop", quickConfirm: "快捷确认",
			confirmationPending: "等待确认", confirmationResult: "确认结果", expired: "已失效", deleted: "此 Loop 已删除",
			deletedHint: "该卡片仅保留删除结果，不提供任何操作。",
			expiredHint: func(at string) string {
				if strings.TrimSpace(at) == "" {
					return "此卡片已失效，最新进展见后续消息"
				}
				return "此卡片已失效，最新进展见后续消息 · " + at
			},
			lifecycleLabel: func(status string) string {
				return map[string]string{
					"backlog": "待规划", "todo": "待处理", "in_progress": "进行中",
					"in_review": "待确认", "done": "已完成", "blocked": "已阻塞", "cancelled": "已取消", "deleted": "已删除",
				}[status]
			},
		}
	}
	return loopLocalizedLabels{
		status: "Status", priority: "Priority", assignee: "Assignee", dueDate: "Due date",
		latestProgress: "Latest progress", revision: "Revision", viewLoop: "View Loop", quickConfirm: "Confirm",
		confirmationPending: "Confirmation pending", confirmationResult: "Confirmation result", expired: "Superseded", deleted: "This Loop was deleted",
		deletedHint: "This card only preserves the deletion result and has no actions.",
		expiredHint: func(at string) string {
			if strings.TrimSpace(at) == "" {
				return "This card is no longer current. See the latest message for progress."
			}
			return "This card is no longer current. See the latest message for progress. · " + at
		},
		lifecycleLabel: func(status string) string {
			return map[string]string{
				"backlog": "Backlog", "todo": "To do", "in_progress": "In progress",
				"in_review": "Pending confirmation", "done": "Done", "blocked": "Blocked", "cancelled": "Cancelled", "deleted": "Deleted",
			}[status]
		},
	}
}
