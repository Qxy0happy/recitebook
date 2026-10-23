package main

import (
	"strings"
	"testing"
)

func TestEmbeddedBankIsConsistent(t *testing.T) {
	bank, err := NewBankService()
	if err != nil {
		t.Fatalf("NewBankService: %v", err)
	}

	summaries := bank.Lists()
	if len(summaries) != 29 {
		t.Fatalf("bank index lists = %d, want 29", len(summaries))
	}
	if len(bank.details) != len(summaries) {
		t.Fatalf("embedded details = %d, want %d", len(bank.details), len(summaries))
	}

	validBind := map[string]bool{"词义": true, "词形": true, "搭配": true, "介词": true, "无目标": true}
	validKind := map[string]bool{
		"single":             true,
		"fill":               true,
		"translate_sentence": true,
		"translate_word":     true,
	}

	var singles, fills, sentences, translateWords int
	for i, summary := range summaries {
		if summary.ListNo != i+1 {
			t.Fatalf("list order = %d at position %d, want %d", summary.ListNo, i, i+1)
		}
		if summary.Title == "" {
			t.Errorf("list %d has an empty index title", summary.ListNo)
		}

		detail, ok := bank.details[summary.ListNo]
		if !ok {
			t.Errorf("list %d is in the index but has no embedded data", summary.ListNo)
			continue
		}
		if detail.ListNo != summary.ListNo {
			t.Errorf("list %d data declares listNo %d", summary.ListNo, detail.ListNo)
		}
		if detail.Title != summary.Title {
			t.Errorf("list %d title = %q, index says %q", summary.ListNo, detail.Title, summary.Title)
		}
		if len(detail.Words) == 0 {
			t.Errorf("list %d has no headwords", summary.ListNo)
		}
		if len(detail.Questions) == 0 {
			t.Errorf("list %d has no questions", summary.ListNo)
		}
		if strings.TrimSpace(detail.Association.Outline) == "" {
			t.Errorf("list %d has no 类目联想 outline", summary.ListNo)
		}
		if len(detail.Association.Scenes) == 0 {
			t.Errorf("list %d has no 类目联想 scenes", summary.ListNo)
		}
		for _, scene := range detail.Association.Scenes {
			if strings.TrimSpace(scene.Title) == "" || strings.TrimSpace(scene.Story) == "" {
				t.Errorf("list %d has a malformed 类目联想 scene: %+v", summary.ListNo, scene)
			}
		}

		headwords := make(map[string]bool, len(detail.Words))
		for _, word := range detail.Words {
			if word.Spelling == "" {
				t.Errorf("list %d has an empty headword", summary.ListNo)
				continue
			}
			if headwords[word.Spelling] {
				t.Errorf("list %d has duplicate headword %q", summary.ListNo, word.Spelling)
			}
			headwords[word.Spelling] = true
		}

		ids := make(map[string]bool, len(detail.Questions))
		for _, q := range detail.Questions {
			if q.ID == "" {
				t.Errorf("list %d has a question without an id", summary.ListNo)
				continue
			}
			if ids[q.ID] {
				t.Errorf("list %d has duplicate question id %q", summary.ListNo, q.ID)
			}
			ids[q.ID] = true
			if !validKind[q.Kind] {
				t.Errorf("list %d question %s has unknown kind %q", summary.ListNo, q.ID, q.Kind)
			}
			if q.Stem == "" {
				t.Errorf("list %d question %s has an empty stem", summary.ListNo, q.ID)
			}
			if q.Reading {
				if len(q.Slots) != 0 {
					t.Errorf("list %d reading question %s has slots", summary.ListNo, q.ID)
				}
				continue
			}
			if len(q.Slots) == 0 {
				t.Errorf("list %d question %s has no bound word", summary.ListNo, q.ID)
			}
			for i, slot := range q.Slots {
				if slot.N != i+1 || slot.Word == "" || !validBind[slot.Bind] {
					t.Errorf("list %d question %s has malformed slot %+v", summary.ListNo, q.ID, slot)
				}
				if !headwords[slot.Word] {
					t.Errorf("list %d question %s binds unknown word %q", summary.ListNo, q.ID, slot.Word)
				}
			}

			switch q.Kind {
			case "single":
				singles++
				if len(q.Options) < 2 {
					t.Errorf("list %d single question %s has %d options", summary.ListNo, q.ID, len(q.Options))
				}
				if q.Answer < 0 || q.Answer >= len(q.Options) {
					t.Errorf("list %d single question %s answer index %d out of range", summary.ListNo, q.ID, q.Answer)
				}
				for _, accept := range q.Accept {
					if accept < 0 || accept >= len(q.Options) {
						t.Errorf("list %d single question %s accept index %d out of range", summary.ListNo, q.ID, accept)
					}
				}
			case "fill":
				fills++
				if len(q.Answers) == 0 {
					t.Errorf("list %d fill question %s has no accepted answer", summary.ListNo, q.ID)
				}
				for _, answer := range q.Answers {
					if answer == "" {
						t.Errorf("list %d fill question %s has an empty accepted answer", summary.ListNo, q.ID)
					}
				}
			case "translate_sentence":
				sentences++
				// Graded by the LLM with a self-assessment fallback; the bank
				// does not have to carry a reference translation.
			case "translate_word":
				translateWords++
				// Graded by the four-level self-assessment; no reference needed.
			}
		}
	}

	if singles == 0 || fills == 0 || sentences == 0 || translateWords == 0 {
		t.Fatalf("question mix missing a kind: single=%d fill=%d sentence=%d word=%d",
			singles, fills, sentences, translateWords)
	}
	t.Logf("29 lists: %d single, %d fill, %d translate_sentence, %d translate_word",
		singles, fills, sentences, translateWords)
}
