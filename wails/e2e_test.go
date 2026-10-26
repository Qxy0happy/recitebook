package main

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"recitebook/internal/engine"
	"recitebook/internal/store"
)

// These tests drive the real EngineService against the embedded 29-list bank
// and a temporary SQLite database. They deliberately bypass HTTP and the
// browser so a full pass over all lists stays fast and deterministic.

func e2eList(t *testing.T, svc *EngineService, listNo int) engine.ListState {
	t.Helper()
	for _, ls := range svc.State().Lists {
		if ls.ListNo == listNo {
			return ls
		}
	}
	t.Fatalf("list %d is not in state", listNo)
	return engine.ListState{}
}

func e2eIDs(questions []engine.Question) []string {
	ids := make([]string, len(questions))
	for i, q := range questions {
		ids[i] = q.ID
	}
	return ids
}

func e2eAnswerAll(t *testing.T, svc *EngineService, correct func(id string) bool) {
	t.Helper()
	for svc.State().Quiz != nil {
		id := svc.State().Quiz.QuestionIDs[svc.State().Quiz.Index]
		if _, err := svc.Answer(correct(id)); err != nil {
			t.Fatalf("Answer(%s): %v", id, err)
		}
	}
}

func TestE2EFirstFullPass(t *testing.T) {
	bank, svc := newTestEngine(t)

	st, err := svc.Enter(1)
	if err != nil {
		t.Fatalf("Enter(1): %v", err)
	}
	want := e2eIDs(bank.Questions(1))
	if st.Quiz == nil {
		t.Fatal("first entry produced no quiz")
	}
	if !slices.Equal(st.Quiz.QuestionIDs, want) {
		t.Fatalf("first quiz = %v, want every book question %v", st.Quiz.QuestionIDs, want)
	}

	e2eAnswerAll(t, svc, func(string) bool { return true })
	st = svc.State()
	if st.Quiz != nil {
		t.Fatalf("quiz still active after the last answer: %+v", st.Quiz)
	}
	ls := e2eList(t, svc, 1)
	if len(ls.Pending) != 0 {
		t.Fatalf("pending after an all-correct pass = %v, want none", ls.Pending)
	}
	if !ls.Completed {
		t.Fatal("list 1 should be completed after an all-correct pass")
	}
	if slices.Contains(st.Queue, 1) {
		t.Fatalf("completed list still in queue: %v", st.Queue)
	}
	if st.Current != 0 {
		t.Fatalf("current = %d, want 0 after completion", st.Current)
	}
	if len(ls.Attempts) != len(want) {
		t.Fatalf("attempts = %d, want %d", len(ls.Attempts), len(want))
	}
}

func TestE2EInnerLoopSizeAndClear(t *testing.T) {
	bank, svc := newTestEngine(t)

	st, err := svc.Enter(1)
	if err != nil {
		t.Fatalf("Enter(1): %v", err)
	}
	ids := e2eIDs(bank.Questions(1))
	if len(ids) < 8 {
		t.Fatalf("list 1 has %d questions, need at least 8 for this scenario", len(ids))
	}
	const wrongCount = 5
	for i, id := range ids {
		if _, err := svc.Answer(i >= wrongCount); err != nil {
			t.Fatalf("Answer(%s): %v", id, err)
		}
	}

	st = svc.State()
	pending := e2eList(t, svc, 1).Pending
	if !slices.Equal(pending, ids[:wrongCount]) {
		t.Fatalf("pending = %v, want the first %d wrong questions %v", pending, wrongCount, ids[:wrongCount])
	}
	if st.Current != 1 {
		t.Fatalf("current = %d, want list 1 while it has pending questions", st.Current)
	}

	inner, err := svc.Enter(0)
	if err != nil {
		t.Fatalf("Enter(0): %v", err)
	}
	if inner.Quiz == nil || inner.Quiz.ListNo != 1 {
		t.Fatalf("inner quiz = %+v, want list 1", inner.Quiz)
	}
	// floor(5 * 1.2) = 6: the five pending questions plus one correct supplement.
	wantSize := wrongCount * 6 / 5
	if len(inner.Quiz.QuestionIDs) != wantSize {
		t.Fatalf("inner quiz = %d questions, want %d", len(inner.Quiz.QuestionIDs), wantSize)
	}
	if !slices.Equal(inner.Quiz.QuestionIDs[:wrongCount], ids[:wrongCount]) {
		t.Fatalf("pending questions lost their book order: %v", inner.Quiz.QuestionIDs)
	}
	if inner.Quiz.QuestionIDs[wrongCount] != ids[wrongCount] {
		t.Fatalf("supplement = %q, want the next correct book question %q", inner.Quiz.QuestionIDs[wrongCount], ids[wrongCount])
	}

	e2eAnswerAll(t, svc, func(string) bool { return true })
	st = svc.State()
	ls := e2eList(t, svc, 1)
	if len(ls.Pending) != 0 {
		t.Fatalf("pending after clearing the inner loop = %v, want none", ls.Pending)
	}
	if !ls.Completed {
		t.Fatal("list 1 should complete when its pending set is empty")
	}
	if slices.Contains(st.Queue, 1) {
		t.Fatalf("completed list still in queue: %v", st.Queue)
	}

	next, err := svc.Enter(0)
	if err != nil {
		t.Fatalf("Enter(0) after completion: %v", err)
	}
	if next.Quiz == nil || next.Quiz.ListNo != 2 {
		t.Fatalf("next quiz = %+v, want list 2", next.Quiz)
	}
}

func TestE2EJumpOutReinsertion(t *testing.T) {
	bank, svc := newTestEngine(t)

	if _, err := svc.Enter(1); err != nil {
		t.Fatalf("Enter(1): %v", err)
	}
	ids := e2eIDs(bank.Questions(1))
	for i, id := range ids {
		if _, err := svc.Answer(i != 0); err != nil {
			t.Fatalf("Answer(%s): %v", id, err)
		}
	}
	if got := e2eList(t, svc, 1).Pending; !slices.Equal(got, ids[:1]) {
		t.Fatalf("pending = %v, want %v", got, ids[:1])
	}

	inner, err := svc.Enter(0)
	if err != nil {
		t.Fatalf("Enter(0): %v", err)
	}
	if inner.Quiz == nil || inner.Quiz.ListNo != 1 {
		t.Fatalf("inner quiz = %+v, want list 1", inner.Quiz)
	}
	jumped, err := svc.JumpOut()
	if err != nil {
		t.Fatalf("JumpOut: %v", err)
	}
	if jumped.Quiz != nil || jumped.Current != 0 {
		t.Fatalf("jump out state: quiz=%+v current=%d", jumped.Quiz, jumped.Current)
	}
	if jumped.Queue[len(jumped.Queue)-1] != 1 {
		t.Fatalf("queue = %v, want list 1 at the tail", jumped.Queue)
	}
	if got := e2eList(t, svc, 1); len(got.Pending) != 1 || got.Completed {
		t.Fatalf("list 1 after jump out = %+v, want one pending, not completed", got)
	}
	if got := e2eList(t, svc, 1); !slices.Equal(got.Group, ids[:1]) || got.GroupAt != 0 {
		t.Fatalf("suspended group = %v/%d, want %v at 0", got.Group, got.GroupAt, ids[:1])
	}

	again, err := svc.Enter(1)
	if err != nil {
		t.Fatalf("Enter(1) after jump out: %v", err)
	}
	if again.Quiz == nil || !slices.Equal(again.Quiz.QuestionIDs, ids[:1]) || again.Quiz.Index != 0 {
		t.Fatalf("re-entry quiz = %+v, want the pending question at index 0", again.Quiz)
	}
	if _, err := svc.Answer(true); err != nil {
		t.Fatalf("Answer after resume: %v", err)
	}
	if got := e2eList(t, svc, 1); !got.Completed || len(got.Pending) != 0 {
		t.Fatalf("list 1 after clearing the suspended group = %+v, want completed", got)
	}
}

func TestE2ELookupMarksWholeQuestionWrong(t *testing.T) {
	bank, svc := newTestEngine(t)

	st, err := svc.Enter(1)
	if err != nil {
		t.Fatalf("Enter(1): %v", err)
	}
	questionID := st.Quiz.QuestionIDs[0]
	question, ok := bank.Question(1, questionID)
	if !ok {
		t.Fatalf("question %s missing from bank", questionID)
	}
	if len(question.Slots) == 0 {
		t.Fatalf("question %s has no bound words", questionID)
	}

	looked, err := svc.Lookup(questionID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if looked.Quiz == nil || looked.Quiz.Index != 1 {
		t.Fatalf("lookup did not advance the quiz: %+v", looked.Quiz)
	}
	if got := e2eList(t, svc, 1).Pending; !slices.Contains(got, questionID) {
		t.Fatalf("pending = %v, want the looked-up question", got)
	}
	attempts := e2eList(t, svc, 1).Attempts
	last := attempts[len(attempts)-1]
	if last.QuestionID != questionID || last.Result != engine.Wrong || last.Via != engine.ViaLookup {
		t.Fatalf("lookup attempt = %+v, want wrong via lookup", last)
	}

	for _, word := range question.Slots {
		var found bool
		for _, ws := range svc.WordStates(1) {
			if ws.Spelling == word {
				found = true
				if ws.Result != engine.Wrong {
					t.Fatalf("word %s result = %q, want wrong", word, ws.Result)
				}
			}
		}
		if !found {
			t.Fatalf("word %s missing from WordStates", word)
		}
	}

	// A second lookup on a question that already advanced must not record a
	// second wrong attempt.
	before := svc.State()
	if _, err := svc.Lookup(questionID); err != nil {
		t.Fatalf("repeated Lookup: %v", err)
	}
	if after := svc.State(); !reflect.DeepEqual(after, before) {
		t.Fatalf("repeated lookup changed state:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestE2ENewRoundRetestsFirstWrongs(t *testing.T) {
	bank, svc := newTestEngine(t)
	listNos := bank.ListNumbers()
	if len(listNos) != 29 {
		t.Fatalf("bank has %d lists, want 29", len(listNos))
	}

	ids := e2eIDs(bank.Questions(1))
	if len(ids) < 8 {
		t.Fatalf("list 1 has %d questions, need at least 8", len(ids))
	}
	st, err := svc.Enter(1)
	if err != nil {
		t.Fatalf("Enter(1): %v", err)
	}
	const firstWrongCount = 5
	for i := range st.Quiz.QuestionIDs {
		if _, err := svc.Answer(i >= firstWrongCount); err != nil {
			t.Fatalf("list 1 answer %d: %v", i, err)
		}
	}
	if got := e2eList(t, svc, 1).FirstWrong; !slices.Equal(got, ids[:firstWrongCount]) {
		t.Fatalf("first wrong = %v, want %v", got, ids[:firstWrongCount])
	}

	// Clear them all; the first-wrong baseline must survive.
	if _, err := svc.Enter(0); err != nil {
		t.Fatalf("Enter(0) for the inner loop: %v", err)
	}
	e2eAnswerAll(t, svc, func(string) bool { return true })
	if got := e2eList(t, svc, 1).Pending; len(got) != 0 {
		t.Fatalf("pending after clearing = %v", got)
	}
	if got := e2eList(t, svc, 1).FirstWrong; !slices.Equal(got, ids[:firstWrongCount]) {
		t.Fatalf("first wrong after clearing = %v, want %v", got, ids[:firstWrongCount])
	}

	// Every other list needs one attempt this round; jumping out with no
	// pending questions completes it.
	for _, listNo := range listNos[1:] {
		if _, err := svc.Enter(listNo); err != nil {
			t.Fatalf("Enter(%d): %v", listNo, err)
		}
		if _, err := svc.Answer(true); err != nil {
			t.Fatalf("Answer on list %d: %v", listNo, err)
		}
		if _, err := svc.JumpOut(); err != nil {
			t.Fatalf("JumpOut on list %d: %v", listNo, err)
		}
	}
	if !svc.CanStartRound() {
		t.Fatal("round should be ready after every list got an attempt")
	}

	round2, err := svc.StartRound()
	if err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if round2.Round != 2 {
		t.Fatalf("round = %d, want 2", round2.Round)
	}
	if !slices.Equal(round2.Queue, []int{1}) {
		t.Fatalf("round 2 queue = %v, want [1]", round2.Queue)
	}
	if got := e2eList(t, svc, 1); !slices.Equal(got.Pending, ids[:firstWrongCount]) || len(got.FirstWrong) != 0 {
		t.Fatalf("list 1 round 2 baseline = pending %v firstWrong %v", got.Pending, got.FirstWrong)
	}
	for _, listNo := range listNos[1:] {
		ls := e2eList(t, svc, listNo)
		if !ls.Completed || len(ls.Pending) != 0 {
			t.Fatalf("list %d after round start = %+v, want completed with no pending", listNo, ls)
		}
	}

	entered, err := svc.Enter(0)
	if err != nil {
		t.Fatalf("Enter(0) in round 2: %v", err)
	}
	want := append([]string(nil), ids[:firstWrongCount]...)
	// floor(5 * 1.2) = 6, so one previous-round correct answer supplements.
	want = append(want, ids[firstWrongCount])
	if entered.Quiz == nil || !slices.Equal(entered.Quiz.QuestionIDs, want) {
		t.Fatalf("round 2 quiz = %+v, want %v", entered.Quiz, want)
	}
}

func TestE2EExportThenImportIntoNewDatabase(t *testing.T) {
	bank, source := newTestEngine(t)

	if _, err := source.Enter(1); err != nil {
		t.Fatalf("Enter(1): %v", err)
	}
	if _, err := source.Answer(false); err != nil {
		t.Fatalf("Answer(false): %v", err)
	}
	if _, err := source.Answer(true); err != nil {
		t.Fatalf("Answer(true): %v", err)
	}
	settings, ok := source.db.(store.SettingsStore)
	if !ok {
		t.Fatal("engine store does not expose settings")
	}
	if _, err := NewGradeService(settings).SaveSettings("https://example.test", "gpt-e2e", "sk-e2e-secret"); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	want := source.State()

	path := filepath.Join(t.TempDir(), "backup.json")
	if err := NewBackupService(source).Export(path); err != nil {
		t.Fatalf("Export: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	text := string(raw)
	if strings.Contains(text, "sk-e2e-secret") || strings.Contains(text, store.SettingAPIKey) {
		t.Fatalf("backup leaked the API key: %s", text)
	}
	if !strings.Contains(text, "https://example.test") {
		t.Fatalf("backup dropped non-secret settings: %s", text)
	}

	target, _, warning, err := openEngine(bank, filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("open fresh database: %v", err)
	}
	if warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
	defer closeEngine(t, target)
	if target.State().Round != 1 || len(target.State().Lists[0].Attempts) != 0 {
		t.Fatalf("fresh database is not empty: %+v", target.State())
	}

	got, err := NewBackupService(target).Import(path)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("imported state mismatch:\ngot  %+v\nwant %+v", got, want)
	}
	if !reflect.DeepEqual(target.State(), want) {
		t.Fatalf("engine state after import mismatch:\ngot  %+v\nwant %+v", target.State(), want)
	}
}
