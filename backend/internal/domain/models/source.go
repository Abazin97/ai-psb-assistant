package models

type Source struct {
	Ordinal     int    `json:"ordinal"`
	DocID       string `json:"doc_id"`
	DocTitle    string `json:"doc_title"`
	FragmentID  string `json:"fragment_id"`
	Text        string `json:"text"`
	StartOffset *int   `json:"start,omitempty"`
	EndOffset   *int   `json:"end,omitempty"`
}
