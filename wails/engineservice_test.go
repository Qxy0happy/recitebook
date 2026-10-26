package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"recitebook/internal/engine"
	"recitebook/internal/store"
)

func TestAnswerSurvivesRestart(t *testing.T) {
	bank, err := NewBankService()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.db")

	first, _, warning, err := openEngine(bank, path)
	if err != nil {
		t.Fatalf("openEngine: %v", err)
	}
	if warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
	if _, err := first.Enter(1); err != nil {
		t.Fatalf("Enter: %v", err)
	}
	want, err := first.Answer(false)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	closeEngine(t, first)

	reopened, _, warning, err := openEngine(bank, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
	defer closeEngine(t, reopened)
	got := reopened.State()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("state after restart:\ngot  %+v\nwant %+v", got, want)
	}
	if got.Quiz == nil || got.Quiz.Index != 1 {
		t.Fatalf("quiz position not restored: %+v", got.Quiz)
	}
	if len(got.Lists[0].Attempts) != 1 || got.Lists[0].Attempts[0].Result != "wrong" {
		t.Fatalf("attempt not restored: %+v", got.Lists[0].Attempts)
	}
}

func TestInnerLoopAndJumpOutSurviveRestart(t *testing.T) {
	bank, err := NewBankService()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.db")

	svc, _, _, err := openEngine(bank, path)
	if err != nil {
		t.Fatalf("openEngine: %v", err)
	}
	defer func() {
		if svc != nil {
			closeEngine(t, svc)
		}
	}()
	st, err := svc.Enter(1)
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	total := len(st.Quiz.QuestionIDs)
	if total < 2 {
		t.Fatalf("list 1 has %d questions, need at least 2", total)
	}
	for i := range total {
		if _, err := svc.Answer(i == total-1); err != nil {
			t.Fatalf("Answer %d: %v", i, err)
		}
	}
	afterPass := svc.State()
	if afterPass.Quiz != nil {
		t.Fatal("first pass should be finished")
	}
	pending := afterPass.Lists[0].Pending
	if len(pending) != total-1 {
		t.Fatalf("pending = %d, want %d", len(pending), total-1)
	}
	if afterPass.Current != 1 {
		t.Fatalf("current = %d, want list 1 while pending remains", afterPass.Current)
	}

	inner, err := svc.Enter(0)
	if err != nil {
		t.Fatalf("inner Enter: %v", err)
	}
	if inner.Quiz == nil || inner.Quiz.ListNo != 1 {
		t.Fatalf("inner quiz = %+v, want list 1", inner.Quiz)
	}
	wantSize := len(pending) * 6 / 5
	if total < wantSize {
		wantSize = total
	}
	if len(inner.Quiz.QuestionIDs) != wantSize {
		t.Fatalf("inner quiz = %d questions, want %d", len(inner.Quiz.QuestionIDs), wantSize)
	}

	jumped, err := svc.JumpOut()
	if err != nil {
		t.Fatalf("JumpOut: %v", err)
	}
	if jumped.Quiz != nil || jumped.Current != 0 {
		t.Fatalf("jump out state: quiz=%+v current=%d", jumped.Quiz, jumped.Current)
	}
	if got := storedPending(t, jumped); got != len(pending) {
		t.Fatalf("pending after jump out = %d, want %d", got, len(pending))
	}
	if len(jumped.Queue) == 0 || jumped.Queue[len(jumped.Queue)-1] != 1 {
		t.Fatalf("queue = %v, want list 1 at the tail", jumped.Queue)
	}
	closeEngine(t, svc)
	svc = nil

	reopened, _, warning, err := openEngine(bank, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeEngine(t, reopened)
	if warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
	if got := reopened.State(); !reflect.DeepEqual(got, jumped) {
		t.Fatalf("state after restart:\ngot  %+v\nwant %+v", got, jumped)
	}
}

func storedPending(t *testing.T, st engine.State) int {
	t.Helper()
	for _, ls := range st.Lists {
		if ls.ListNo == 1 {
			return len(ls.Pending)
		}
	}
	t.Fatal("list 1 not in state")
	return 0
}

func TestCorruptDatabaseIsBackedUpAndReported(t *testing.T) {
	bank, err := NewBankService()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc, _, warning, err := openEngine(bank, path)
	if err != nil {
		t.Fatalf("openEngine: %v", err)
	}
	defer closeEngine(t, svc)
	if !strings.Contains(warning, "数据库文件损坏") {
		t.Fatalf("warning = %q, want corruption notice", warning)
	}
	if !strings.Contains(warning, ".corrupt-") {
		t.Fatalf("warning does not name backup: %q", warning)
	}
	matches, err := filepath.Glob(path + ".corrupt-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("backup file: %v %v", matches, err)
	}
	if len(svc.State().Queue) == 0 {
		t.Fatalf("recovered store has no queue: %+v", svc.State())
	}
}

func closeEngine(t *testing.T, svc *EngineService) {
	t.Helper()
	if c, ok := svc.db.(interface{ Close() error }); ok {
		if err := c.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}
}

func TestLookupMarksCurrentQuestionWrongAndPersists(t *testing.T) {
	bank, err := NewBankService()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.db")

	svc, _, _, err := openEngine(bank, path)
	if err != nil {
		t.Fatalf("openEngine: %v", err)
	}
	st, err := svc.Enter(1)
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	questionID := st.Quiz.QuestionIDs[0]
	question, ok := bank.Question(1, questionID)
	if !ok || len(question.Slots) == 0 {
		t.Fatalf("question %s has no bound words", questionID)
	}

	looked, err := svc.Lookup(questionID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if looked.Quiz == nil || looked.Quiz.Index != 1 {
		t.Fatalf("quiz index after lookup = %+v, want 1", looked.Quiz)
	}
	attempts := looked.Lists[0].Attempts
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].QuestionID != questionID || attempts[0].Result != engine.Wrong || attempts[0].Via != engine.ViaLookup {
		t.Fatalf("attempt = %+v, want %s wrong via lookup", attempts[0], questionID)
	}
	if !containsID(looked.Lists[0].Pending, questionID) {
		t.Fatalf("pending = %v, want %s", looked.Lists[0].Pending, questionID)
	}
	for _, word := range question.Slots {
		if got := wordResult(svc.WordStates(1), word); got != engine.Wrong {
			t.Fatalf("%s = %q, want wrong", word, got)
		}
	}
	closeEngine(t, svc)

	reopened, _, warning, err := openEngine(bank, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeEngine(t, reopened)
	if warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
	if got := reopened.State(); !reflect.DeepEqual(got, looked) {
		t.Fatalf("lookup not persisted:\ngot  %+v\nwant %+v", got, looked)
	}
}

func TestRepeatedLookupIsIgnored(t *testing.T) {
	bank, err := NewBankService()
	if err != nil {
		t.Fatal(err)
	}
	svc, _, _, err := openEngine(bank, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("openEngine: %v", err)
	}
	defer closeEngine(t, svc)

	st, err := svc.Enter(1)
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	ids := st.Quiz.QuestionIDs

	// A stale question id must not touch the state.
	if _, err := svc.Lookup(ids[1]); err != nil {
		t.Fatalf("Lookup stale question: %v", err)
	}
	if got := svc.State(); len(got.Lists[0].Attempts) != 0 || got.Quiz.Index != 0 {
		t.Fatalf("stale lookup changed state: attempts=%d index=%d", len(got.Lists[0].Attempts), got.Quiz.Index)
	}

	for range 3 {
		if _, err := svc.Lookup(ids[0]); err != nil {
			t.Fatalf("Lookup: %v", err)
		}
	}
	got := svc.State()
	if len(got.Lists[0].Attempts) != 1 {
		t.Fatalf("repeated lookup recorded %d attempts, want 1", len(got.Lists[0].Attempts))
	}
	if got.Quiz == nil || got.Quiz.Index != 1 {
		t.Fatalf("repeated lookup advanced to %+v, want index 1", got.Quiz)
	}
}

func TestLookupOnTranslationQuestionMarksQuestionWrong(t *testing.T) {
	bank, err := NewBankService()
	if err != nil {
		t.Fatal(err)
	}
	svc, _, _, err := openEngine(bank, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("openEngine: %v", err)
	}
	defer closeEngine(t, svc)

	detail := bank.Detail(1)
	translateID := ""
	for _, q := range detail.Questions {
		if q.Kind == "translate_sentence" {
			translateID = q.ID
			break
		}
	}
	if translateID == "" {
		t.Fatal("list 1 has no sentence translation question")
	}
	question, _ := bank.Question(1, translateID)

	st, err := svc.Enter(1)
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	for st.Quiz != nil && st.Quiz.QuestionIDs[st.Quiz.Index] != translateID {
		st, err = svc.Answer(true)
		if err != nil {
			t.Fatalf("Answer: %v", err)
		}
	}
	if st.Quiz == nil {
		t.Fatal("translation question was not reached")
	}

	looked, err := svc.Lookup(translateID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	attempts := looked.Lists[0].Attempts
	last := attempts[len(attempts)-1]
	if last.QuestionID != translateID || last.Result != engine.Wrong || last.Via != engine.ViaLookup {
		t.Fatalf("last attempt = %+v, want translation question wrong via lookup", last)
	}
	if !containsID(looked.Lists[0].Pending, translateID) {
		t.Fatalf("pending = %v, want %s", looked.Lists[0].Pending, translateID)
	}
	for _, word := range question.Slots {
		if got := wordResult(svc.WordStates(1), word); got != engine.Wrong {
			t.Fatalf("%s = %q, want wrong", word, got)
		}
	}
}

func TestWordStatesOnlyReturnsThisListHeadwords(t *testing.T) {
	bank, err := NewBankService()
	if err != nil {
		t.Fatal(err)
	}
	svc, _, _, err := openEngine(bank, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("openEngine: %v", err)
	}
	defer closeEngine(t, svc)

	got := svc.WordStates(1)
	own := bank.Detail(1).Words
	if len(got) != len(own) {
		t.Fatalf("word states = %d, want %d headwords from list 1", len(got), len(own))
	}
	for i, word := range own {
		if got[i].Spelling != word.Spelling {
			t.Fatalf("word state %d = %s, want %s", i, got[i].Spelling, word.Spelling)
		}
	}
	for _, word := range bank.Detail(2).Words {
		for _, row := range got {
			if row.Spelling == word.Spelling {
				t.Fatalf("word %s from list 2 leaked into list 1 states", word.Spelling)
			}
		}
	}
}

func containsID(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

func wordResult(states []WordState, spelling string) engine.Result {
	for _, state := range states {
		if state.Spelling == spelling {
			return state.Result
		}
	}
	return ""
}

func TestStartRoundSurvivesRestart(t *testing.T) {
	bank, err := NewBankService()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.db")

	// Seed a finished first round: every list has one attempt, list 1 missed
	// its first question.
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seed := engine.New(bank.ListNumbers())
	for i, listNo := range bank.ListNumbers() {
		questions := bank.Questions(listNo)
		if len(questions) == 0 {
			continue
		}
		ls := &seed.Lists[i]
		result := engine.Correct
		if listNo == 1 {
			result = engine.Wrong
		}
		seed.Seq++
		attempt := engine.Attempt{
			Round:      1,
			QuestionID: questions[0].ID,
			Result:     result,
			Via:        engine.ViaAnswer,
			Seq:        seed.Seq,
		}
		ls.Attempts = append(ls.Attempts, attempt)
		if err := db.AppendAttempt(listNo, attempt); err != nil {
			t.Fatalf("seed attempt: %v", err)
		}
		if result == engine.Wrong {
			ls.Pending = []string{questions[0].ID}
			ls.FirstWrong = []string{questions[0].ID}
		}
	}
	if err := db.Save(seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	svc, _, warning, err := openEngine(bank, path)
	if err != nil {
		t.Fatalf("openEngine: %v", err)
	}
	defer func() {
		if svc != nil {
			closeEngine(t, svc)
		}
	}()
	if warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
	if !svc.CanStartRound() {
		t.Fatal("every list has an attempt; a new round should be available")
	}
	started, err := svc.StartRound()
	if err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if started.Round != 2 {
		t.Fatalf("round = %d, want 2", started.Round)
	}
	if !reflect.DeepEqual(started.Queue, []int{1}) {
		t.Fatalf("queue = %v, want [1]", started.Queue)
	}
	if started.Quiz != nil || started.Current != 0 {
		t.Fatalf("quiz/current not cleared: %+v/%d", started.Quiz, started.Current)
	}
	if svc.CanStartRound() {
		t.Fatal("new round should not be startable before its lists are attempted")
	}
	list1 := started.Lists[0]
	if !reflect.DeepEqual(list1.Pending, []string{bank.Questions(1)[0].ID}) {
		t.Fatalf("baseline pending = %v", list1.Pending)
	}
	if len(list1.FirstWrong) != 0 || list1.Completed {
		t.Fatalf("list 1 not reset: %+v", list1)
	}
	if !started.Lists[1].Completed {
		t.Fatalf("list 2 should be complete in the new round: %+v", started.Lists[1])
	}
	closeEngine(t, svc)
	svc = nil

	reopened, _, warning, err := openEngine(bank, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeEngine(t, reopened)
	if warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
	got := reopened.State()
	if !reflect.DeepEqual(got, started) {
		t.Fatalf("state after restart:\ngot  %+v\nwant %+v", got, started)
	}
	history := got.Lists[0].Attempts
	if len(history) != 1 || history[0].Round != 1 || history[0].Result != engine.Wrong {
		t.Fatalf("round-one history lost: %+v", history)
	}
}

func TestAnswerRecordsScoreAndSource(t *testing.T) {
	bank, err := NewBankService()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.db")
	svc, _, _, err := openEngine(bank, path)
	if err != nil {
		t.Fatalf("openEngine: %v", err)
	}
	if _, err := svc.Enter(1); err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if _, err := svc.Answer(true); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if _, err := svc.AnswerLLM(false, 35); err != nil {
		t.Fatalf("AnswerLLM: %v", err)
	}
	st, err := svc.AnswerSelf(true, 80)
	if err != nil {
		t.Fatalf("AnswerSelf: %v", err)
	}

	want := []struct {
		result engine.Result
		via    engine.Via
		score  int
	}{
		{engine.Correct, engine.ViaAnswer, 100},
		{engine.Wrong, engine.ViaLLM, 35},
		{engine.Correct, engine.ViaSelf, 80},
	}
	attempts := st.Lists[0].Attempts
	if len(attempts) != len(want) {
		t.Fatalf("attempts = %d, want %d", len(attempts), len(want))
	}
	for i, exp := range want {
		got := attempts[i]
		if got.Result != exp.result || got.Via != exp.via || got.Score != exp.score {
			t.Fatalf("attempt %d = %+v, want result %q via %q score %d", i, got, exp.result, exp.via, exp.score)
		}
	}
	closeEngine(t, svc)

	reopened, _, warning, err := openEngine(bank, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeEngine(t, reopened)
	if warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
	if got := reopened.State(); !reflect.DeepEqual(got, st) {
		t.Fatalf("state after restart:\ngot  %+v\nwant %+v", got, st)
	}
}

func TestPreviewMarksListAcrossRestartWithoutStartingQuiz(t *testing.T) {
	bank, err := NewBankService()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.db")

	first, _, _, err := openEngine(bank, path)
	if err != nil {
		t.Fatalf("openEngine: %v", err)
	}
	previewed, err := first.Preview(1)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if previewed.Quiz != nil {
		t.Fatalf("preview started a quiz: %+v", previewed.Quiz)
	}
	if !previewed.Lists[0].Previewed {
		t.Fatalf("list 1 not marked: %+v", previewed.Lists[0])
	}
	closeEngine(t, first)

	reopened, _, warning, err := openEngine(bank, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeEngine(t, reopened)
	if warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
	if !reopened.State().Lists[0].Previewed {
		t.Fatal("preview mark lost across restart")
	}
	entered, err := reopened.Enter(1)
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if entered.Quiz == nil || entered.Quiz.ListNo != 1 || len(entered.Quiz.QuestionIDs) == 0 {
		t.Fatalf("quiz after preview = %+v", entered.Quiz)
	}
}

func TestSuspendedGroupResumesAfterRestart(t *testing.T) {
	bank, err := NewBankService()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.db")

	svc, _, _, err := openEngine(bank, path)
	if err != nil {
		t.Fatalf("openEngine: %v", err)
	}
	entered, err := svc.Enter(1)
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if entered.Quiz == nil || len(entered.Quiz.QuestionIDs) < 2 {
		t.Fatalf("list 1 quiz = %+v, need at least 2 questions", entered.Quiz)
	}
	ids := append([]string(nil), entered.Quiz.QuestionIDs...)
	if _, err := svc.Answer(true); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	jumped, err := svc.JumpOut()
	if err != nil {
		t.Fatalf("JumpOut: %v", err)
	}
	ls := jumped.Lists[0]
	if ls.Completed || !reflect.DeepEqual(ls.Group, ids) || ls.GroupAt != 1 {
		t.Fatalf("jump out state = completed:%v group:%v at:%d", ls.Completed, ls.Group, ls.GroupAt)
	}
	closeEngine(t, svc)

	reopened, _, warning, err := openEngine(bank, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeEngine(t, reopened)
	if warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
	if got := reopened.State(); !reflect.DeepEqual(got, jumped) {
		t.Fatalf("state after restart:\ngot  %+v\nwant %+v", got, jumped)
	}

	resumed, err := reopened.Enter(1)
	if err != nil {
		t.Fatalf("resume Enter: %v", err)
	}
	if resumed.Quiz == nil || resumed.Quiz.Index != 1 || !reflect.DeepEqual(resumed.Quiz.QuestionIDs, ids) {
		t.Fatalf("resumed quiz = %+v, want the same group at index 1", resumed.Quiz)
	}

	cur := resumed
	for cur.Quiz != nil {
		cur, err = reopened.Answer(true)
		if err != nil {
			t.Fatalf("Answer after resume: %v", err)
		}
	}
	if !cur.Lists[0].Completed {
		t.Fatal("finishing the resumed group with no pending must complete the list")
	}
	for _, listNo := range cur.Queue {
		if listNo == 1 {
			t.Fatalf("queue = %v, completed list 1 must not be queued", cur.Queue)
		}
	}
	if len(cur.Queue) != 28 {
		t.Fatalf("queue = %v, want the other 28 lists", cur.Queue)
	}
}
