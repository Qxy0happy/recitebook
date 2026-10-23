package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"recitebook/internal/engine"
)

//go:embed bank/lists.json
var bankIndex []byte

//go:embed bank/list-*.json
var bankData embed.FS

// ListSummary is the queue-facing description of one list.
type ListSummary struct {
	ListNo int    `json:"list_no"`
	Title  string `json:"title"`
}

// Sense is one part-of-speech meaning of a headword.
type Sense struct {
	Pos       string `json:"pos"`
	MeaningCN string `json:"meaning_cn"`
}

// Note is one 考点点拨 entry.
type Note struct {
	Marker   string `json:"marker"`
	Kind     string `json:"kind"`
	LabelRaw string `json:"label_raw"`
	Body     string `json:"body"`
}

// Word is one headword shown in a list.
type Word struct {
	Spelling string  `json:"spelling"`
	IPA      string  `json:"ipa"`
	Senses   []Sense `json:"senses"`
	Mnemonic string  `json:"mnemonic"`
	Notes    []Note  `json:"notes"`
}

// Slot binds one blank to a headword and the knowledge being tested.
type Slot struct {
	N    int    `json:"n"`
	Word string `json:"word"`
	Bind string `json:"bind"`
}

// Question is one quiz item.
type Question struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Stem    string   `json:"stem"`
	Options []string `json:"options"`
	Answer  int      `json:"answer"`
	Accept  []int    `json:"accept"`
	Answers []string `json:"answers"`
	Slots   []Slot   `json:"slots"`
	Reading bool     `json:"reading"`
}

// Scene is one 类目联想 story: the scene heading and its paragraph.
type Scene struct {
	Title string `json:"title"`
	Story string `json:"story"`
}

// Association is the 类目联想 opening of a list: the plot outline and the scene
// stories that carry the group's vocabulary.
type Association struct {
	Outline string  `json:"outline"`
	Scenes  []Scene `json:"scenes"`
}

// ListDetail is everything the quiz screen needs for one list.
type ListDetail struct {
	ListNo      int         `json:"list_no"`
	Title       string      `json:"title"`
	Association Association `json:"association"`
	Words       []Word      `json:"words"`
	Questions   []Question  `json:"questions"`
}

type bankFile struct {
	Lists []ListSummary `json:"lists"`
}

// BankService exposes the embedded question bank to the frontend.
type BankService struct {
	lists   []ListSummary
	details map[int]ListDetail
}

// NewBankService parses the embedded bank once at startup.
func NewBankService() (*BankService, error) {
	var index bankFile
	if err := json.Unmarshal(bankIndex, &index); err != nil {
		return nil, fmt.Errorf("parse bank index: %w", err)
	}
	if len(index.Lists) == 0 {
		return nil, fmt.Errorf("bank index is empty")
	}

	details := make(map[int]ListDetail)
	entries, err := fs.ReadDir(bankData, "bank")
	if err != nil {
		return nil, fmt.Errorf("read bank directory: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "list-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := bankData.ReadFile(path.Join("bank", name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		var detail ListDetail
		if err := json.Unmarshal(data, &detail); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		if detail.ListNo == 0 {
			return nil, fmt.Errorf("%s has no list_no", name)
		}
		if _, exists := details[detail.ListNo]; exists {
			return nil, fmt.Errorf("duplicate data for list %d", detail.ListNo)
		}
		details[detail.ListNo] = detail
	}

	return &BankService{lists: index.Lists, details: details}, nil
}

// Lists returns every list in reading order.
func (b *BankService) Lists() []ListSummary {
	return b.lists
}

// ListNumbers returns list numbers in reading order.
func (b *BankService) ListNumbers() []int {
	numbers := make([]int, len(b.lists))
	for i, l := range b.lists {
		numbers[i] = l.ListNo
	}
	return numbers
}

// Detail returns the words and questions for one list. Missing lists yield an empty detail.
func (b *BankService) Detail(listNo int) ListDetail {
	return b.details[listNo]
}

// Questions implements engine.Bank.
func (b *BankService) Questions(listNo int) []engine.Question {
	d := b.details[listNo]
	out := make([]engine.Question, len(d.Questions))
	for i, q := range d.Questions {
		out[i] = engine.Question{ID: q.ID, Slots: slotWords(q.Slots)}
	}
	return out
}

// Question implements engine.Bank.
func (b *BankService) Question(listNo int, id string) (engine.Question, bool) {
	for _, q := range b.details[listNo].Questions {
		if q.ID == id {
			return engine.Question{ID: q.ID, Slots: slotWords(q.Slots)}, true
		}
	}
	return engine.Question{}, false
}

func slotWords(slots []Slot) []string {
	words := make([]string, 0, len(slots))
	for _, slot := range slots {
		if slot.Word != "" {
			words = append(words, slot.Word)
		}
	}
	return words
}
