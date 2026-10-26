package main

import (
	"strconv"
	"strings"
)

// chatReplyLimit caps the student answer and the graded outcome in runes so a
// pasted essay cannot crowd the word cards out of the prompt.
const chatReplyLimit = 500

// chatAnswerMissing is printed whenever the bank has no usable answer.
const chatAnswerMissing = "题库未提供"

var chatKindLabels = map[string]string{
	"single":             "单选",
	"fill":               "填空",
	"translate_sentence": "整句翻译",
	"translate_word":     "单词翻译",
}

// chatContext renders the system context for asking about one question: the
// list heading, the question with its resolved answer, the student's answer and
// the graded outcome, then the bound word cards. It is a pure function so the
// prompt stays rebuildable from the bank on every request.
func chatContext(detail ListDetail, question Question, userAnswer, outcome string) string {
	lines := []string{
		"List " + strconv.Itoa(detail.ListNo) + " · " + detail.Title,
		"题型：" + chatKindLabel(question.Kind),
		"题干：" + question.Stem,
	}
	if len(question.Options) > 0 {
		lines = append(lines, "选项："+chatOptions(question.Options))
	}
	lines = append(lines,
		"正确答案："+chatAnswer(question),
		"我的作答："+chatReply(userAnswer, "未作答"),
		"判定："+chatReply(outcome, "未记录"),
	)

	words := chatSlotWords(question.Slots)
	if len(words) == 0 {
		lines = append(lines, "绑定词：无")
		return strings.Join(lines, "\n")
	}

	lines = append(lines, "绑定词：")
	for _, word := range words {
		lines = append(lines, chatWordCard(detail.Words, word))
	}
	return strings.Join(lines, "\n")
}

// chatKindLabel renders the bank kind with its Chinese label, keeping unknown
// kinds untouched.
func chatKindLabel(kind string) string {
	if label, ok := chatKindLabels[kind]; ok {
		return kind + "（" + label + "）"
	}
	return kind
}

// chatOptions numbers the choices A, B, C… for the 选项 line.
func chatOptions(options []string) string {
	parts := make([]string, 0, len(options))
	for i, option := range options {
		parts = append(parts, string(rune('A'+i))+". "+option)
	}
	return strings.Join(parts, " / ")
}

// chatAnswer resolves the correct answer according to the question kind.
func chatAnswer(question Question) string {
	switch question.Kind {
	case "single":
		return chatSingleAnswer(question)
	case "fill":
		if len(question.Answers) == 0 {
			return chatAnswerMissing
		}
		return strings.Join(question.Answers, " / ")
	case "translate_word":
		return "见词卡释义"
	default:
		if len(question.Answers) == 0 {
			return chatAnswerMissing
		}
		return question.Answers[0]
	}
}

// chatSingleAnswer resolves options[answer] and lists any other accepted
// choices.
func chatSingleAnswer(question Question) string {
	if question.Answer < 0 || question.Answer >= len(question.Options) {
		return chatAnswerMissing
	}

	accepted := make([]string, 0, len(question.Accept))
	for _, index := range question.Accept {
		if index == question.Answer || index < 0 || index >= len(question.Options) {
			continue
		}
		accepted = append(accepted, question.Options[index])
	}
	if len(accepted) == 0 {
		return question.Options[question.Answer]
	}
	return question.Options[question.Answer] + "（也接受：" + strings.Join(accepted, " / ") + "）"
}

// chatReply trims the student's answer or the graded outcome, falls back to a
// placeholder when blank, and truncates by rune to keep UTF-8 intact.
func chatReply(text, fallback string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return fallback
	}
	runes := []rune(text)
	if len(runes) > chatReplyLimit {
		runes = runes[:chatReplyLimit]
	}
	return string(runes)
}

// chatSlotWords lists the bound headwords once each, keeping slot order and
// folding case so duplicate slots do not repeat a card.
func chatSlotWords(slots []Slot) []string {
	words := make([]string, 0, len(slots))
	seen := make(map[string]bool, len(slots))
	for _, word := range slotWords(slots) {
		key := strings.ToLower(word)
		if seen[key] {
			continue
		}
		seen[key] = true
		words = append(words, word)
	}
	return words
}

// chatWordCard renders one bound word, matching the card by spelling without
// regard to case. An unmatched headword is printed alone.
func chatWordCard(words []Word, headword string) string {
	for _, word := range words {
		if !strings.EqualFold(word.Spelling, headword) {
			continue
		}

		lines := []string{"- " + word.Spelling}
		if ipa := chatIPA(word.IPA); ipa != "" {
			lines[0] += " " + ipa
		}
		if senses := chatSenses(word.Senses); senses != "" {
			lines = append(lines, "  释义："+senses)
		}
		if mnemonic := strings.TrimSpace(word.Mnemonic); mnemonic != "" {
			lines = append(lines, "  妙记："+mnemonic)
		}
		if notes := chatNotes(word.Notes); notes != "" {
			lines = append(lines, "  考点："+notes)
		}
		return strings.Join(lines, "\n")
	}
	return "- " + headword
}

// chatIPA trims the bank IPA and wraps it in slashes only when the stored
// value does not already carry its own /…/ or […] delimiters.
func chatIPA(ipa string) string {
	ipa = strings.TrimSpace(ipa)
	if ipa == "" {
		return ""
	}
	if (strings.HasPrefix(ipa, "/") || strings.HasPrefix(ipa, "[")) &&
		(strings.HasSuffix(ipa, "/") || strings.HasSuffix(ipa, "]")) {
		return ipa
	}
	return "/" + ipa + "/"
}

// chatSenses joins every sense as "pos meaning_cn".
func chatSenses(senses []Sense) string {
	parts := make([]string, 0, len(senses))
	for _, sense := range senses {
		if part := chatFields(sense.Pos, sense.MeaningCN); part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "；")
}

// chatNotes joins every 考点 as "marker kind label_raw body".
func chatNotes(notes []Note) string {
	parts := make([]string, 0, len(notes))
	for _, note := range notes {
		if part := chatFields(note.Marker, note.Kind, note.LabelRaw, note.Body); part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "；")
}

// chatFields joins the non-blank fields with single spaces.
func chatFields(fields ...string) string {
	kept := make([]string, 0, len(fields))
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			kept = append(kept, field)
		}
	}
	return strings.Join(kept, " ")
}
