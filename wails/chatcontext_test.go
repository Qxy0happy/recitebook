package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func chatTestDetail() ListDetail {
	return ListDetail{
		ListNo: 3,
		Title:  "变化状态与态度评价",
		Words: []Word{
			{
				Spelling: "intelligence",
				IPA:      "ɪnˈtelɪdʒəns",
				Senses: []Sense{
					{Pos: "n.", MeaningCN: "智力"},
					{Pos: "n.", MeaningCN: "智能"},
				},
				Mnemonic: "intel（在……之间）+ ligence（选择）",
				Notes: []Note{
					{Marker: "①", Kind: "其他", LabelRaw: "不可数", Body: "抽象名词不可数"},
					{Marker: "②", Kind: "搭配", Body: "artificial intelligence"},
				},
			},
			{
				Spelling: "intellect",
				IPA:      "ˈɪntəlekt",
				Senses:   []Sense{{Pos: "n.", MeaningCN: "智力"}},
			},
		},
	}
}

func TestChatContextSingleWithWordCard(t *testing.T) {
	question := Question{
		Kind:    "single",
		Stem:    "Companies increasingly rely on artificial ____ to optimize supply chain management.",
		Options: []string{"intelligence", "intellect", "intuition", "gene"},
		Answer:  0,
		Slots:   []Slot{{N: 1, Word: "intelligence"}},
	}

	got := chatContext(chatTestDetail(), question, "intellect", "答错")

	want := `List 3 · 变化状态与态度评价
题型：single（单选）
题干：Companies increasingly rely on artificial ____ to optimize supply chain management.
选项：A. intelligence / B. intellect / C. intuition / D. gene
正确答案：intelligence
我的作答：intellect
判定：答错
绑定词：
- intelligence /ɪnˈtelɪdʒəns/
  释义：n. 智力；n. 智能
  妙记：intel（在……之间）+ ligence（选择）
  考点：① 其他 不可数 抽象名词不可数；② 搭配 artificial intelligence`

	if got != want {
		t.Fatalf("chatContext =\n%s\nwant:\n%s", got, want)
	}
}

func TestChatContextSingleListsAcceptedAnswers(t *testing.T) {
	question := Question{
		Kind:    "single",
		Stem:    "Pick one.",
		Options: []string{"alpha", "beta", "gamma"},
		Answer:  0,
		Accept:  []int{1, 0, 9},
	}

	got := chatContext(ListDetail{ListNo: 1, Title: "Test"}, question, "", "")

	if !strings.Contains(got, "正确答案：alpha（也接受：beta）") {
		t.Fatalf("accepted answer line missing from:\n%s", got)
	}
}

func TestChatContextFillJoinsAnswers(t *testing.T) {
	question := Question{
		Kind:    "fill",
		Stem:    "The committee will ____ the proposal.",
		Answers: []string{"carry out", "implement"},
		Slots:   []Slot{{N: 1, Word: "Intellect"}},
	}

	got := chatContext(chatTestDetail(), question, " carry out ", "答对")

	for _, want := range []string{
		"题型：fill（填空）",
		"正确答案：carry out / implement",
		"我的作答：carry out",
		"- intellect /ˈɪntəlekt/\n  释义：n. 智力",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("chatContext missing %q:\n%s", want, got)
		}
	}
}

func TestChatContextTranslateWord(t *testing.T) {
	question := Question{
		Kind:    "translate_word",
		Stem:    "翻译单词：intelligence",
		Answers: []string{"智力"},
	}

	got := chatContext(chatTestDetail(), question, "", "")

	for _, want := range []string{
		"题型：translate_word（单词翻译）",
		"正确答案：见词卡释义",
		"我的作答：未作答",
		"判定：未记录",
		"绑定词：无",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("chatContext missing %q:\n%s", want, got)
		}
	}
}

func TestChatContextTranslateSentence(t *testing.T) {
	question := Question{
		Kind:    "translate_sentence",
		Stem:    "翻译：Companies rely on intelligence.",
		Answers: []string{"公司依赖智能。"},
	}

	got := chatContext(chatTestDetail(), question, "公司依靠智能。", "80 分；扣分点：rely on 译错")

	for _, want := range []string{
		"题型：translate_sentence（整句翻译）",
		"正确答案：公司依赖智能。",
		"判定：80 分；扣分点：rely on 译错",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("chatContext missing %q:\n%s", want, got)
		}
	}
}

func TestChatContextMissingWord(t *testing.T) {
	question := Question{
		Kind:    "single",
		Stem:    "Pick one.",
		Options: []string{"absent"},
		Slots:   []Slot{{N: 1, Word: "absent"}, {N: 2, Word: "intellect"}},
	}

	got := chatContext(chatTestDetail(), question, "absent", "答对")

	if !strings.Contains(got, "绑定词：\n- absent\n- intellect /ˈɪntəlekt/") {
		t.Fatalf("unmatched slot word should render as a bare headword:\n%s", got)
	}
}

func TestChatContextDedupesSlotWords(t *testing.T) {
	question := Question{
		Kind:  "fill",
		Stem:  "Pick one.",
		Slots: []Slot{{N: 1, Word: "intellect"}, {N: 2, Word: "Intellect"}, {N: 3, Word: "intellect"}},
	}

	got := chatContext(chatTestDetail(), question, "", "")

	if n := strings.Count(got, "- intellect"); n != 1 {
		t.Fatalf("slot words rendered %d times, want 1:\n%s", n, got)
	}
}

func TestChatContextTruncates(t *testing.T) {
	long := strings.Repeat("汉", 600)

	got := chatContext(
		ListDetail{ListNo: 1, Title: "Test"},
		Question{Kind: "fill", Stem: "Pick one.", Answers: []string{"答案"}},
		long,
		long,
	)

	if !utf8.ValidString(got) {
		t.Fatal("chatContext produced invalid UTF-8")
	}

	lines := strings.Split(got, "\n")
	for _, prefix := range []string{"我的作答：", "判定："} {
		var line string
		for _, candidate := range lines {
			if strings.HasPrefix(candidate, prefix) {
				line = candidate
				break
			}
		}
		if line == "" {
			t.Fatalf("missing line with prefix %q:\n%s", prefix, got)
		}
		if n := utf8.RuneCountInString(strings.TrimPrefix(line, prefix)); n != 500 {
			t.Fatalf("%s rune count = %d, want 500", prefix, n)
		}
	}
}

func TestChatContextMissingBankData(t *testing.T) {
	cases := []struct {
		name     string
		question Question
	}{
		{"single without options", Question{Kind: "single", Stem: "Pick one.", Answer: 2}},
		{"single with out-of-range answer", Question{Kind: "single", Stem: "Pick one.", Options: []string{"alpha"}, Answer: 3}},
		{"fill without answers", Question{Kind: "fill", Stem: "Pick one."}},
		{"translate_sentence without answers", Question{Kind: "translate_sentence", Stem: "Pick one."}},
		{"unknown kind without answers", Question{Kind: "cloze", Stem: "Pick one."}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chatContext(ListDetail{ListNo: 1, Title: "Test"}, tc.question, "  ", " ")
			if !strings.Contains(got, "正确答案：题库未提供") {
				t.Fatalf("chatContext missing fallback answer:\n%s", got)
			}
			if len(tc.question.Options) == 0 && strings.Contains(got, "选项：") {
				t.Fatalf("empty options should omit the 选项 line:\n%s", got)
			}
			if !strings.Contains(got, "我的作答：未作答") || !strings.Contains(got, "判定：未记录") {
				t.Fatalf("blank answer/outcome should fall back to 未作答/未记录:\n%s", got)
			}
		})
	}
}

func TestChatIPAWrapsOnce(t *testing.T) {
	cases := []struct {
		name string
		ipa  string
		want string
	}{
		{"bare", "ɪnˈtelɪdʒəns", "/ɪnˈtelɪdʒəns/"},
		{"stored slashes", "/ɪnˈtelɪdʒəns/", "/ɪnˈtelɪdʒəns/"},
		{"stored brackets", "[ɪnˈtelɪdʒəns]", "[ɪnˈtelɪdʒəns]"},
		{"trimmed", "  /ˈɪntəlekt/ ", "/ˈɪntəlekt/"},
		{"empty", "  ", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chatIPA(tc.ipa); got != tc.want {
				t.Fatalf("chatIPA(%q) = %q, want %q", tc.ipa, got, tc.want)
			}
		})
	}
}

func TestChatWordCardKeepsStoredSlashes(t *testing.T) {
	words := []Word{{Spelling: "intelligence", IPA: "/ɪnˈtelɪdʒəns/"}}

	got := chatWordCard(words, "Intelligence")

	if want := "- intelligence /ɪnˈtelɪdʒəns/"; got != want {
		t.Fatalf("chatWordCard = %q, want %q", got, want)
	}
}

func TestChatContextUnknownKindKeepsKind(t *testing.T) {
	question := Question{Kind: "cloze", Stem: "Pick one.", Answers: []string{"答案"}}

	got := chatContext(ListDetail{ListNo: 1, Title: "Test"}, question, "", "")

	if !strings.Contains(got, "题型：cloze") {
		t.Fatalf("unknown kind should be printed as-is:\n%s", got)
	}
	if !strings.Contains(got, "正确答案：答案") {
		t.Fatalf("unknown kind should fall back to answers[0]:\n%s", got)
	}
}
