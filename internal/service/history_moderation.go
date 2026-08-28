package service

import (
	"github.com/shopspring/decimal"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
)

// HistoryModerationStatus 是作者可见的历史版本审核结果；机审未通过不细分原因。
type HistoryModerationStatus string

const (
	// HistoryModerationPassed 表示该历史版本的机器审核通过。
	HistoryModerationPassed HistoryModerationStatus = "passed"
	// HistoryModerationMachineFailed 折叠机器 review 与 block，避免向作者泄露严重度。
	HistoryModerationMachineFailed HistoryModerationStatus = "machine_failed"
)

// HistoryModerationView 按访问者能力裁剪历史版本的机器审核结论。
type HistoryModerationView struct {
	Status  HistoryModerationStatus  `json:"status"`
	Verdict *model.ModerationVerdict `json:"verdict,omitempty"`
	Score   *decimal.Decimal         `json:"score,omitempty"`
}

func historyModerationByRevision(
	records []model.ModerationRecord,
	includeDetails bool,
) map[int32]*HistoryModerationView {
	views := make(map[int32]*HistoryModerationView, len(records))
	for index := range records {
		record := &records[index]
		if record.ContentRevision == nil {
			continue
		}
		status := HistoryModerationMachineFailed
		if record.Verdict == model.ModerationVerdictPass {
			status = HistoryModerationPassed
		}
		view := &HistoryModerationView{Status: status}
		if includeDetails {
			verdict := record.Verdict
			view.Verdict = &verdict
			view.Score = record.Score
		}
		views[*record.ContentRevision] = view
	}
	return views
}
