package service

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"backend/internal/domain/models"
)

// MockML mimics the ML HTTP contract for local frontend/backend development.
// Its sample answers come from the example dialogues shown in the frontend.
type MockML struct{}

func NewMockML() *MockML { return &MockML{} }

func (m *MockML) Answer(ctx context.Context, request AnswerRequest) (AnswerResponse, error) {
	if err := ctx.Err(); err != nil {
		return AnswerResponse{}, err
	}
	question := strings.ToLower(request.Question)

	switch {
	case strings.Contains(question, "аренд") || strings.Contains(question, "северный склад"):
		return AnswerResponse{
			Kind:   "answer",
			Answer: "Арендная плата — 450 000 руб. в месяц, включая НДС (п. 3.1 договора аренды А-08/2024). Вносится ежемесячно, не позднее 5-го числа текущего месяца (п. 3.2).",
			Meta:   json.RawMessage(`{"mode":"mock","scenario":"lease-a-08-2024"}`),
			Sources: []models.Source{{
				Ordinal: 1, DocID: "lease-a-08-2024", DocTitle: "Договор аренды А-08/2024",
				FragmentID: "п. 3.1, 3.2",
				Text:       "Арендная плата — 450 000 руб. в месяц, включая НДС. Вносится ежемесячно, не позднее 5-го числа текущего месяца.",
			}},
		}, nil

	case strings.Contains(question, "сезон") || strings.Contains(question, "срочн"):
		return AnswerResponse{
			Kind:   "answer",
			Answer: "Да, срочный договор допускается для сезонных работ, когда по природным условиям работа может выполняться только в течение определённого периода (сезона) (ст. 59 ТК РФ, ч. 1). Нужно, чтобы работа действительно была сезонной по природным условиям. В тексте есть пометка об изменении статьи с 01.03.2027.",
			Meta:   json.RawMessage(`{"mode":"mock","scenario":"tk-59-seasonal-work"}`),
			Sources: []models.Source{{
				Ordinal: 1, DocID: "tk-59", DocTitle: "ТК РФ",
				FragmentID: "ст. 59, ч. 1",
				Text:       "Срочный трудовой договор допускается для сезонных работ, если по природным условиям работу можно выполнять только в течение определённого периода (сезона).",
			}},
		}, nil

	default:
		return AnswerResponse{
			Kind:   "clarification",
			Answer: "Мок-сервис получил вопрос, но для этого сценария нет подготовленного ответа. Сейчас доступны примеры по арендной плате в договоре А-08/2024 и срочному договору для сезонных работ.",
			Meta:   json.RawMessage(`{"mode":"mock","scenario":"unsupported"}`),
		}, nil
	}
}

func (m *MockML) Document(ctx context.Context, docID string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var text string
	switch docID {
	case "lease-a-08-2024":
		text = "Договор аренды А-08/2024\nп. 3.1. Арендная плата составляет 450 000 руб. в месяц, включая НДС.\nп. 3.2. Арендная плата вносится ежемесячно, не позднее 5-го числа текущего месяца."
	case "tk-59":
		text = "ТК РФ, статья 59, часть 1. Срочный трудовой договор допускается для сезонных работ, когда по природным условиям работа может выполняться только в течение определённого периода (сезона)."
	default:
		return nil, ErrDocumentNotFound
	}
	return io.NopCloser(strings.NewReader(text)), nil
}

func (m *MockML) Health(ctx context.Context) (MLHealth, error) {
	if err := ctx.Err(); err != nil {
		return MLHealth{}, err
	}
	return MLHealth{Status: "ok", KBHash: "mock", Chunks: 2, Mode: "mock"}, nil
}
