package engine

import (
	"fmt"
	"reflect"
	"testing"
)

type fakeBank struct {
	questions map[int][]Question
}

func (f fakeBank) Questions(listNo int) []Question {
	return f.questions[listNo]
}

func (f fakeBank) Question(listNo int, id string) (Question, bool) {
	for _, q := range f.questions[listNo] {
		if q.ID == id {
			return q, true
		}
	}
	return Question{}, false
}

func testBank() fakeBank {
	return fakeBank{questions: map[int][]Question{
		1: {
			{ID: "q1", Slots: []string{"alpha"}},
			{ID: "q2", Slots: []string{"alpha", "beta"}},
			{ID: "q3", Slots: []string{"gamma"}},
		},
		2: {
			{ID: "q4", Slots: []string{"delta"}},
		},
	}}
}

// bankWith builds a deterministic bank: list N has questions LN-Q01..LN-Qnn,
// each bound to the word LN-Wnn.
func bankWith(sizes map[int]int) fakeBank {
	bank := fakeBank{questions: map[int][]Question{}}
	for listNo, size := range sizes {
		for i := 1; i <= size; i++ {
			bank.questions[listNo] = append(bank.questions[listNo], Question{
				ID:    fmt.Sprintf("L%d-Q%02d", listNo, i),
				Slots: []string{fmt.Sprintf("L%d-W%02d", listNo, i)},
			})
		}
	}
	return bank
}

func answerQuiz(t *testing.T, st State, bank Bank, correct func(id string) bool) State {
	t.Helper()
	for st.Quiz != nil {
		quiz := *st.Quiz
		id := quiz.QuestionIDs[quiz.Index]
		var err error
		st, err = Apply(st, bank, Answer{Correct: correct(id)})
		if err != nil {
			t.Fatalf("Answer(%s): %v", id, err)
		}
	}
	return st
}

func firstPassWrong(count int) func(id string) bool {
	return func(id string) bool {
		var n int
		if _, err := fmt.Sscanf(id[len(id)-2:], "%02d", &n); err != nil {
			return true
		}
		return n > count
	}
}

func pendingOf(t *testing.T, st State, listNo int) []string {
	t.Helper()
	for _, ls := range st.Lists {
		if ls.ListNo == listNo {
			return ls.Pending
		}
	}
	t.Fatalf("list %d not in state", listNo)
	return nil
}

func TestFirstEntryUsesEveryQuestion(t *testing.T) {
	bank := testBank()
	st := New([]int{1, 2})

	st, err := Apply(st, bank, Enter{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Current != 1 {
		t.Fatalf("current = %d, want 1", st.Current)
	}
	if len(st.Quiz.QuestionIDs) != 3 {
		t.Fatalf("questions = %d, want 3", len(st.Quiz.QuestionIDs))
	}
}

func TestAnswerWrongMarksEveryBoundWordWrong(t *testing.T) {
	bank := testBank()
	st := New([]int{1})
	st, _ = Apply(st, bank, Enter{})
	st, _ = Apply(st, bank, Answer{Correct: true})
	st, _ = Apply(st, bank, Answer{Correct: false})

	if got := WordResult(st, bank, 1, "alpha"); got != Wrong {
		t.Fatalf("alpha = %q, want wrong", got)
	}
	if got := WordResult(st, bank, 1, "beta"); got != Wrong {
		t.Fatalf("beta = %q, want wrong", got)
	}
}

func TestLastAttemptWins(t *testing.T) {
	bank := testBank()
	st := New([]int{1})
	st, _ = Apply(st, bank, Enter{})
	st, _ = Apply(st, bank, Answer{Correct: false})
	st, _ = Apply(st, bank, Answer{Correct: true})

	if got := WordResult(st, bank, 1, "alpha"); got != Correct {
		t.Fatalf("alpha = %q, want correct", got)
	}
}

func TestCompletingQuizAdvancesQueue(t *testing.T) {
	bank := testBank()
	st := New([]int{1, 2})
	st, _ = Apply(st, bank, Enter{})
	for range 3 {
		st, _ = Apply(st, bank, Answer{Correct: true})
	}
	if st.Quiz != nil {
		t.Fatal("quiz should be finished")
	}
	if len(st.Queue) != 1 || st.Queue[0] != 2 {
		t.Fatalf("queue = %v, want [2]", st.Queue)
	}
	if !st.Lists[0].Completed {
		t.Fatal("list 1 should be completed")
	}
}

func TestEnterSpecificList(t *testing.T) {
	bank := testBank()
	st := New([]int{1, 2})
	st, err := Apply(st, bank, Enter{ListNo: 2})
	if err != nil {
		t.Fatal(err)
	}
	if st.Current != 2 || st.Quiz.ListNo != 2 {
		t.Fatalf("current = %d, quiz = %d, want 2", st.Current, st.Quiz.ListNo)
	}
}

func TestAnswerWithoutQuizFails(t *testing.T) {
	bank := testBank()
	st := New([]int{1})
	if _, err := Apply(st, bank, Answer{Correct: true}); err == nil {
		t.Fatal("expected error")
	}
}

func TestJumpOutWithoutQuizFails(t *testing.T) {
	bank := testBank()
	st := New([]int{1})
	if _, err := Apply(st, bank, JumpOut{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestInnerLoopQuizSize(t *testing.T) {
	cases := []struct {
		name      string
		bankSize  int
		firstPass int // number of leading questions answered wrong on first pass
		wantQuiz  int
	}{
		{name: "five pending adds one supplement", bankSize: 10, firstPass: 5, wantQuiz: 6},
		{name: "seven pending adds one supplement", bankSize: 20, firstPass: 7, wantQuiz: 8},
		{name: "three pending rounds down to three", bankSize: 20, firstPass: 3, wantQuiz: 3},
		{name: "one pending rounds down to one", bankSize: 6, firstPass: 1, wantQuiz: 1},
		{name: "no correct questions caps the group", bankSize: 5, firstPass: 5, wantQuiz: 5},
		{name: "small correct pool caps the group", bankSize: 11, firstPass: 10, wantQuiz: 11},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bank := bankWith(map[int]int{1: tc.bankSize})
			st := New([]int{1})
			st, err := Apply(st, bank, Enter{})
			if err != nil {
				t.Fatal(err)
			}
			st = answerQuiz(t, st, bank, firstPassWrong(tc.firstPass))

			pending := pendingOf(t, st, 1)
			if len(pending) != tc.firstPass {
				t.Fatalf("pending = %d, want %d", len(pending), tc.firstPass)
			}
			if tc.firstPass > 0 && st.Lists[0].Completed {
				t.Fatal("list with pending questions must not complete")
			}

			st, err = Apply(st, bank, Enter{})
			if err != nil {
				t.Fatal(err)
			}
			if st.Quiz == nil {
				t.Fatal("expected an inner-loop quiz")
			}
			ids := st.Quiz.QuestionIDs
			if len(ids) != tc.wantQuiz {
				t.Fatalf("quiz size = %d, want %d (ids %v)", len(ids), tc.wantQuiz, ids)
			}
			seen := map[string]bool{}
			for _, id := range ids {
				if seen[id] {
					t.Fatalf("duplicate question %s in %v", id, ids)
				}
				seen[id] = true
			}
			for i, id := range pending {
				if ids[i] != id {
					t.Fatalf("pending question %d = %s, want %s (ids %v)", i, ids[i], id, ids)
				}
			}
		})
	}
}

func TestCorrectAnswerClearsPending(t *testing.T) {
	bank := bankWith(map[int]int{1: 10})
	st := New([]int{1})
	st, _ = Apply(st, bank, Enter{})
	st = answerQuiz(t, st, bank, firstPassWrong(5))

	st, err := Apply(st, bank, Enter{})
	if err != nil {
		t.Fatal(err)
	}
	inner := append([]string(nil), st.Quiz.QuestionIDs...)
	if len(inner) != 6 {
		t.Fatalf("inner quiz = %v, want 6 questions", inner)
	}
	st, err = Apply(st, bank, Answer{Correct: true}) // clears L1-Q01
	if err != nil {
		t.Fatal(err)
	}
	st = answerQuiz(t, st, bank, func(string) bool { return false })

	pending := pendingOf(t, st, 1)
	if len(pending) != 5 {
		t.Fatalf("pending = %v, want 5 questions", pending)
	}
	for _, id := range pending {
		if id == inner[0] {
			t.Fatalf("cleared question %s still pending", id)
		}
	}
	if st.Quiz != nil {
		t.Fatal("quiz should be finished")
	}
	if st.Current != 1 {
		t.Fatalf("current = %d, want 1 while pending remains", st.Current)
	}

	st, err = Apply(st, bank, Enter{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Quiz.ListNo != 1 {
		t.Fatalf("default enter list = %d, want 1", st.Quiz.ListNo)
	}
	for i, id := range pending {
		if st.Quiz.QuestionIDs[i] != id {
			t.Fatalf("next quiz question %d = %s, want %s", i, st.Quiz.QuestionIDs[i], id)
		}
	}
}

func TestEnterDefaultsToPendingList(t *testing.T) {
	bank := bankWith(map[int]int{1: 4, 2: 2})
	st := New([]int{1, 2})
	st, _ = Apply(st, bank, Enter{})
	st = answerQuiz(t, st, bank, func(string) bool { return false })

	if st.Current != 1 {
		t.Fatalf("current = %d, want 1", st.Current)
	}
	if len(st.Queue) != 2 || st.Queue[0] != 1 {
		t.Fatalf("queue = %v, want [1 2]", st.Queue)
	}
	st, err := Apply(st, bank, Enter{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Current != 1 || st.Quiz.ListNo != 1 {
		t.Fatalf("default enter = %d/%d, want list 1", st.Current, st.Quiz.ListNo)
	}
	if len(st.Quiz.QuestionIDs) != 4 {
		t.Fatalf("inner quiz = %d, want 4", len(st.Quiz.QuestionIDs))
	}
}

func TestEnterWithoutPendingCompletesList(t *testing.T) {
	bank := bankWith(map[int]int{1: 2, 2: 1})
	st := New([]int{1, 2})
	st, _ = Apply(st, bank, Enter{})
	st = answerQuiz(t, st, bank, func(string) bool { return true })

	if !st.Lists[0].Completed || len(st.Queue) != 1 || st.Queue[0] != 2 {
		t.Fatalf("first pass should complete: completed=%v queue=%v", st.Lists[0].Completed, st.Queue)
	}
	st, err := Apply(st, bank, Enter{ListNo: 1})
	if err != nil {
		t.Fatalf("Enter completed list: %v", err)
	}
	if st.Quiz != nil {
		t.Fatalf("quiz = %+v, want none", st.Quiz)
	}
	if !st.Lists[0].Completed {
		t.Fatal("list should stay completed")
	}
	if len(st.Queue) != 1 || st.Queue[0] != 2 {
		t.Fatalf("queue = %v, want [2]", st.Queue)
	}
}

func TestJumpOutMovesListToQueueTail(t *testing.T) {
	bank := bankWith(map[int]int{1: 3, 2: 2, 3: 1})
	st := New([]int{1, 2, 3})
	st, _ = Apply(st, bank, Enter{})
	st, _ = Apply(st, bank, Answer{Correct: false})
	st, err := Apply(st, bank, JumpOut{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Quiz != nil {
		t.Fatal("jump out must end the quiz")
	}
	if st.Current != 0 {
		t.Fatalf("current = %d, want 0", st.Current)
	}
	if len(st.Queue) != 3 || st.Queue[0] != 2 || st.Queue[1] != 3 || st.Queue[2] != 1 {
		t.Fatalf("queue = %v, want [2 3 1]", st.Queue)
	}
	if got := len(pendingOf(t, st, 1)); got != 1 {
		t.Fatalf("pending = %d, want 1", got)
	}
	if st.Lists[0].Completed {
		t.Fatal("list with pending questions must not complete")
	}

	st, _ = Apply(st, bank, Enter{})
	if st.Current != 2 {
		t.Fatalf("next default list = %d, want 2", st.Current)
	}
}

func TestJumpOutWithNoPendingCompletes(t *testing.T) {
	bank := bankWith(map[int]int{1: 2, 2: 1})
	st := New([]int{1, 2})
	st, _ = Apply(st, bank, Enter{})
	st, err := Apply(st, bank, JumpOut{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Quiz != nil {
		t.Fatal("jump out must end the quiz")
	}
	if !st.Lists[0].Completed {
		t.Fatal("list with no pending questions should complete")
	}
	if len(st.Queue) != 1 || st.Queue[0] != 2 {
		t.Fatalf("queue = %v, want [2]", st.Queue)
	}
	if st.Current != 0 {
		t.Fatalf("current = %d, want 0", st.Current)
	}
}

func TestFirstWrongKeepsFirstWrongAttempt(t *testing.T) {
	bank := bankWith(map[int]int{1: 3})
	st := New([]int{1})
	st, _ = Apply(st, bank, Enter{})
	st = answerQuiz(t, st, bank, firstPassWrong(1))

	if got := st.Lists[0].FirstWrong; len(got) != 1 || got[0] != "L1-Q01" {
		t.Fatalf("first wrong = %v, want [L1-Q01]", got)
	}
	st, _ = Apply(st, bank, Enter{})
	st = answerQuiz(t, st, bank, func(string) bool { return true })
	if got := st.Lists[0].FirstWrong; len(got) != 1 || got[0] != "L1-Q01" {
		t.Fatalf("first wrong after clearing = %v, want [L1-Q01]", got)
	}
	if !st.Lists[0].Completed {
		t.Fatal("list should complete once pending is empty")
	}
}

func linkedBank() fakeBank {
	return fakeBank{questions: map[int][]Question{
		1: {
			{ID: "q1", Slots: []string{"alpha"}},
			{ID: "q2", Slots: []string{"alpha", "beta"}},
			{ID: "q3", Slots: []string{"alpha"}},
		},
	}}
}

func TestWordResultTracksLastAttemptPerWord(t *testing.T) {
	cases := []struct {
		name  string
		steps []Answer
		want  map[string]Result
	}{
		{
			name:  "correct then wrong",
			steps: []Answer{{Correct: true}, {Correct: false}},
			want:  map[string]Result{"alpha": Wrong, "beta": Wrong},
		},
		{
			name:  "wrong then correct",
			steps: []Answer{{Correct: false}, {Correct: true}},
			want:  map[string]Result{"alpha": Correct, "beta": Correct},
		},
		{
			name:  "same word in three questions ends wrong",
			steps: []Answer{{Correct: true}, {Correct: true}, {Correct: false}},
			want:  map[string]Result{"alpha": Wrong, "beta": Correct},
		},
		{
			name:  "same word in three questions ends correct",
			steps: []Answer{{Correct: false}, {Correct: false}, {Correct: true}},
			want:  map[string]Result{"alpha": Correct, "beta": Wrong},
		},
		{
			name:  "lookup counts as wrong",
			steps: []Answer{{Correct: false, Via: ViaLookup}},
			want:  map[string]Result{"alpha": Wrong},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bank := linkedBank()
			st := New([]int{1})
			var err error
			st, err = Apply(st, bank, Enter{})
			if err != nil {
				t.Fatal(err)
			}
			for i, step := range tc.steps {
				st, err = Apply(st, bank, step)
				if err != nil {
					t.Fatalf("step %d: %v", i, err)
				}
			}
			for word, want := range tc.want {
				if got := WordResult(st, bank, 1, word); got != want {
					t.Fatalf("%s = %q, want %q", word, got, want)
				}
			}
		})
	}
}

func TestLookupRecordsViaAndPending(t *testing.T) {
	bank := linkedBank()
	st := New([]int{1})
	st, _ = Apply(st, bank, Enter{})
	st, err := Apply(st, bank, Answer{Correct: false, Via: ViaLookup})
	if err != nil {
		t.Fatal(err)
	}

	attempts := st.Lists[0].Attempts
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].Result != Wrong || attempts[0].Via != ViaLookup {
		t.Fatalf("attempt = %+v, want wrong via lookup", attempts[0])
	}
	if pending := st.Lists[0].Pending; len(pending) != 1 || pending[0] != "q1" {
		t.Fatalf("pending = %v, want [q1]", pending)
	}
}

// pass enters a list and answers its whole quiz with the given results,
// including any inner-loop quiz the engine builds.
func pass(t *testing.T, st State, bank Bank, listNo int, correct func(id string) bool) State {
	t.Helper()
	var err error
	st, err = Apply(st, bank, Enter{ListNo: listNo})
	if err != nil {
		t.Fatalf("Enter(%d): %v", listNo, err)
	}
	return answerQuiz(t, st, bank, correct)
}

func TestCanStartRound(t *testing.T) {
	bank := bankWith(map[int]int{1: 2, 2: 1, 3: 0})
	allCorrect := func(string) bool { return true }

	cases := []struct {
		name  string
		setup func(t *testing.T) State
		want  bool
	}{
		{
			name:  "fresh round is not finished",
			setup: func(*testing.T) State { return New([]int{1, 2, 3}) },
			want:  false,
		},
		{
			name: "one list still unattempted",
			setup: func(t *testing.T) State {
				return pass(t, New([]int{1, 2, 3}), bank, 1, allCorrect)
			},
			want: false,
		},
		{
			name: "zero-question list counts as finished",
			setup: func(t *testing.T) State {
				st := pass(t, New([]int{1, 2, 3}), bank, 1, allCorrect)
				return pass(t, st, bank, 2, allCorrect)
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanStartRound(tc.setup(t), bank); got != tc.want {
				t.Fatalf("CanStartRound = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStartRound(t *testing.T) {
	bank := bankWith(map[int]int{1: 3, 2: 2, 3: 0})
	allCorrect := func(string) bool { return true }

	cases := []struct {
		name           string
		setup          func(t *testing.T) State
		wantErr        bool
		wantRound      int
		wantQueue      []int
		wantPending    map[int][]string
		wantFirstWrong map[int][]string
		wantCompleted  map[int]bool
		wantAttempts   map[int]int
	}{
		{
			name: "forbidden until every list has an attempt",
			setup: func(t *testing.T) State {
				return pass(t, New([]int{1, 2, 3}), bank, 1, allCorrect)
			},
			wantErr: true,
		},
		{
			name: "all lists attempted starts the next round",
			setup: func(t *testing.T) State {
				st := pass(t, New([]int{1, 2, 3}), bank, 1, allCorrect)
				return pass(t, st, bank, 2, allCorrect)
			},
			wantRound:      2,
			wantQueue:      []int{},
			wantPending:    map[int][]string{1: nil, 2: nil, 3: nil},
			wantFirstWrong: map[int][]string{1: nil, 2: nil, 3: nil},
			wantCompleted:  map[int]bool{1: true, 2: true, 3: true},
			wantAttempts:   map[int]int{1: 3, 2: 2, 3: 0},
		},
		{
			name: "cleared wrong questions become the next baseline",
			setup: func(t *testing.T) State {
				st := pass(t, New([]int{1, 2, 3}), bank, 1, firstPassWrong(1))
				st = pass(t, st, bank, 1, allCorrect) // inner loop clears L1-Q01
				return pass(t, st, bank, 2, allCorrect)
			},
			wantRound:      2,
			wantQueue:      []int{1},
			wantPending:    map[int][]string{1: {"L1-Q01"}, 2: nil, 3: nil},
			wantFirstWrong: map[int][]string{1: nil, 2: nil, 3: nil},
			wantCompleted:  map[int]bool{1: false, 2: true, 3: true},
			wantAttempts:   map[int]int{1: 4, 2: 2, 3: 0},
		},
		{
			name: "starting clears the active quiz and current list",
			setup: func(t *testing.T) State {
				st := pass(t, New([]int{1, 2, 3}), bank, 1, firstPassWrong(2))
				st = pass(t, st, bank, 2, allCorrect)
				inner, err := Apply(st, bank, Enter{})
				if err != nil || inner.Quiz == nil {
					t.Fatalf("expected an active inner quiz: %v %+v", err, inner.Quiz)
				}
				return inner
			},
			wantRound:      2,
			wantQueue:      []int{1},
			wantPending:    map[int][]string{1: {"L1-Q01", "L1-Q02"}, 2: nil, 3: nil},
			wantFirstWrong: map[int][]string{1: nil, 2: nil, 3: nil},
			wantCompleted:  map[int]bool{1: false, 2: true, 3: true},
			wantAttempts:   map[int]int{1: 3, 2: 2, 3: 0},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.setup(t)
			got, err := Apply(before, bank, StartRound{})
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				if !reflect.DeepEqual(got, before) {
					t.Fatalf("rejected round changed state:\ngot  %+v\nwant %+v", got, before)
				}
				return
			}
			if err != nil {
				t.Fatalf("StartRound: %v", err)
			}
			if got.Round != tc.wantRound {
				t.Fatalf("round = %d, want %d", got.Round, tc.wantRound)
			}
			if !reflect.DeepEqual(got.Queue, tc.wantQueue) {
				t.Fatalf("queue = %v, want %v", got.Queue, tc.wantQueue)
			}
			if got.Quiz != nil || got.Current != 0 {
				t.Fatalf("quiz/current not cleared: quiz=%+v current=%d", got.Quiz, got.Current)
			}
			for listNo, want := range tc.wantPending {
				if got := listOf(t, got, listNo).Pending; !reflect.DeepEqual(got, want) {
					t.Fatalf("list %d pending = %v, want %v", listNo, got, want)
				}
			}
			for listNo, want := range tc.wantFirstWrong {
				if got := listOf(t, got, listNo).FirstWrong; !reflect.DeepEqual(got, want) {
					t.Fatalf("list %d firstWrong = %v, want %v", listNo, got, want)
				}
			}
			for listNo, want := range tc.wantCompleted {
				if got := listOf(t, got, listNo).Completed; got != want {
					t.Fatalf("list %d completed = %v, want %v", listNo, got, want)
				}
			}
			for listNo, want := range tc.wantAttempts {
				if got := len(listOf(t, got, listNo).Attempts); got != want {
					t.Fatalf("list %d attempts = %d, want %d", listNo, got, want)
				}
			}
		})
	}
}

func TestNewRoundRetestsOnlyFirstWrongs(t *testing.T) {
	bank := bankWith(map[int]int{1: 3, 2: 2})
	allCorrect := func(string) bool { return true }

	st := pass(t, New([]int{1, 2}), bank, 1, firstPassWrong(1))
	st = pass(t, st, bank, 1, allCorrect)
	st = pass(t, st, bank, 2, allCorrect)
	st, err := Apply(st, bank, StartRound{})
	if err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if !containsInt(st.Queue, 1) || containsInt(st.Queue, 2) {
		t.Fatalf("queue = %v, want only list 1", st.Queue)
	}

	st, err = Apply(st, bank, Enter{ListNo: 1})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if st.Quiz == nil || !reflect.DeepEqual(st.Quiz.QuestionIDs, []string{"L1-Q01"}) {
		t.Fatalf("new round quiz = %+v, want only the first wrong", st.Quiz)
	}
	// Round-one history is archived, not reset.
	if attempts := listOf(t, st, 1).Attempts; len(attempts) != 4 {
		t.Fatalf("attempts = %d, want 4", len(attempts))
	}
	for _, attempt := range listOf(t, st, 1).Attempts {
		if attempt.Round != 1 {
			t.Fatalf("historical attempt round = %d, want 1", attempt.Round)
		}
	}
}

func TestNewRoundSupplementsPendingWithPreviousRoundCorrectAnswers(t *testing.T) {
	bank := bankWith(map[int]int{1: 6, 2: 1})
	allCorrect := func(string) bool { return true }

	st := New([]int{1, 2})
	st = pass(t, st, bank, 1, firstPassWrong(5))
	if got := len(pendingOf(t, st, 1)); got != 5 {
		t.Fatalf("pending after first pass = %d, want 5", got)
	}
	st = pass(t, st, bank, 1, allCorrect)
	if got := len(pendingOf(t, st, 1)); got != 0 {
		t.Fatalf("pending after inner loop = %d, want 0", got)
	}
	st = pass(t, st, bank, 2, allCorrect)

	if !CanStartRound(st, bank) {
		t.Fatal("round should be ready to start")
	}
	var err error
	st, err = Apply(st, bank, StartRound{})
	if err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	st, err = Apply(st, bank, Enter{ListNo: 1})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}

	want := []string{"L1-Q01", "L1-Q02", "L1-Q03", "L1-Q04", "L1-Q05", "L1-Q06"}
	if st.Quiz == nil || !reflect.DeepEqual(st.Quiz.QuestionIDs, want) {
		t.Fatalf("new round quiz = %+v, want %v", st.Quiz, want)
	}
}

func listOf(t *testing.T, st State, listNo int) ListState {
	t.Helper()
	for _, ls := range st.Lists {
		if ls.ListNo == listNo {
			return ls
		}
	}
	t.Fatalf("list %d not in state", listNo)
	return ListState{}
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func TestCanStartRoundAfterCompletedListHasNoQuestionsThisRound(t *testing.T) {
	bank := bankWith(map[int]int{1: 2, 2: 1})
	allCorrect := func(string) bool { return true }

	// Round 1: list 1 clears completely, so round 2 starts it already completed.
	st := pass(t, New([]int{1, 2}), bank, 1, allCorrect)
	st = pass(t, st, bank, 2, firstPassWrong(1))
	st, err := Apply(st, bank, StartRound{})
	if err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if !listOf(t, st, 1).Completed {
		t.Fatal("list 1 should start round 2 completed")
	}
	if containsInt(st.Queue, 1) {
		t.Fatalf("queue = %v, completed list 1 must not be queued", st.Queue)
	}

	// Round 2 finishes list 2. List 1 never gets an attempt this round, but it
	// has nothing left to answer, so it must not block the following round.
	st = pass(t, st, bank, 2, allCorrect)
	if !CanStartRound(st, bank) {
		t.Fatal("completed list without attempts this round blocks the next round")
	}
	st, err = Apply(st, bank, StartRound{})
	if err != nil {
		t.Fatalf("StartRound after a completed list: %v", err)
	}
	if st.Round != 3 {
		t.Fatalf("round = %d, want 3", st.Round)
	}
}

func TestNewRoundSupplementFallsBackToPreviousRound(t *testing.T) {
	bank := bankWith(map[int]int{1: 6, 2: 1})
	allCorrect := func(string) bool { return true }

	st := pass(t, New([]int{1, 2}), bank, 1, firstPassWrong(5))
	st = pass(t, st, bank, 2, allCorrect)
	st, err := Apply(st, bank, StartRound{})
	if err != nil {
		t.Fatalf("StartRound: %v", err)
	}

	// Round 2 has no results of its own yet, so the supplement comes from
	// round 1: Q06 was the only correct answer there.
	st, err = Apply(st, bank, Enter{ListNo: 1})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	want := []string{"L1-Q01", "L1-Q02", "L1-Q03", "L1-Q04", "L1-Q05", "L1-Q06"}
	if st.Quiz == nil || !reflect.DeepEqual(st.Quiz.QuestionIDs, want) {
		t.Fatalf("new round quiz = %+v, want %v", st.Quiz, want)
	}
}

func TestSupplementPrefersCurrentRoundOverPreviousRound(t *testing.T) {
	bank := bankWith(map[int]int{1: 6, 2: 1})
	allCorrect := func(string) bool { return true }
	// Only Q01 correct, Q02..Q06 wrong.
	onlyFirstCorrect := func(id string) bool { return id == "L1-Q01" }

	st := pass(t, New([]int{1, 2}), bank, 1, firstPassWrong(5))
	st = pass(t, st, bank, 1, allCorrect) // clears round 1
	st = pass(t, st, bank, 2, allCorrect)
	st, err := Apply(st, bank, StartRound{})
	if err != nil {
		t.Fatalf("StartRound: %v", err)
	}

	// Round 2 first quiz: five pending questions plus Q06 as the supplement.
	st, err = Apply(st, bank, Enter{ListNo: 1})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	st = answerQuiz(t, st, bank, onlyFirstCorrect)
	if got := pendingOf(t, st, 1); !reflect.DeepEqual(got, []string{"L1-Q02", "L1-Q03", "L1-Q04", "L1-Q05", "L1-Q06"}) {
		t.Fatalf("pending = %v", got)
	}

	// Q01 is now correct in round 2 although round 1 recorded it wrong, so the
	// inner-loop supplement must come from the current round.
	st = mustEnter(t, st, bank)
	want := []string{"L1-Q02", "L1-Q03", "L1-Q04", "L1-Q05", "L1-Q06", "L1-Q01"}
	if st.Quiz == nil || !reflect.DeepEqual(st.Quiz.QuestionIDs, want) {
		t.Fatalf("inner quiz = %+v, want %v", st.Quiz, want)
	}
}

func TestSupplementIgnoresRoundsOlderThanThePreviousOne(t *testing.T) {
	bank := bankWith(map[int]int{1: 8, 2: 2})
	allCorrect := func(string) bool { return true }

	// Round 1: Q01..Q06 wrong, Q07 and Q08 correct.
	st := pass(t, New([]int{1, 2}), bank, 1, func(id string) bool { return id > "L1-Q06" })
	st = pass(t, st, bank, 2, firstPassWrong(1))
	st, err := Apply(st, bank, StartRound{})
	if err != nil {
		t.Fatalf("StartRound: %v", err)
	}

	// Round 2: the six carried-over questions stay wrong and take Q07 as their
	// supplement; Q08 is never attempted. Jump out to keep them pending.
	st, err = Apply(st, bank, Enter{ListNo: 1})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if st.Quiz == nil || len(st.Quiz.QuestionIDs) != 7 {
		t.Fatalf("round 2 quiz = %+v, want six pending plus one supplement", st.Quiz)
	}
	st = answerQuiz(t, st, bank, func(string) bool { return false })
	if got := len(pendingOf(t, st, 1)); got != 7 {
		t.Fatalf("round 2 pending = %d, want 7", got)
	}
	// Finish list 2 so round 3 is allowed.
	st, err = Apply(st, bank, Enter{ListNo: 2})
	if err != nil {
		t.Fatalf("Enter(2): %v", err)
	}
	st = answerQuiz(t, st, bank, allCorrect)
	if !CanStartRound(st, bank) {
		t.Fatal("round 2 should be complete")
	}
	st, err = Apply(st, bank, StartRound{})
	if err != nil {
		t.Fatalf("StartRound: %v", err)
	}

	// Round 3: Q08 was only answered in round 1, which is older than the
	// previous round, so it is not a valid supplement anymore.
	st, err = Apply(st, bank, Enter{ListNo: 1})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if st.Quiz == nil || containsString(st.Quiz.QuestionIDs, "L1-Q08") {
		t.Fatalf("round 3 quiz = %+v, want no stale Q08 supplement", st.Quiz)
	}
	if len(st.Quiz.QuestionIDs) != 7 {
		t.Fatalf("round 3 quiz size = %d, want only the seven pending questions", len(st.Quiz.QuestionIDs))
	}
}

func mustEnter(t *testing.T, st State, bank Bank) State {
	t.Helper()
	next, err := Apply(st, bank, Enter{})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	return next
}

func TestAttemptRecordsScoreAndVia(t *testing.T) {
	bank := testBank()
	cases := []struct {
		name       string
		answer     Answer
		wantResult Result
		wantVia    Via
		wantScore  int
	}{
		{"auto correct scores 100", Answer{Correct: true}, Correct, ViaAnswer, 100},
		{"auto wrong scores 0", Answer{Correct: false}, Wrong, ViaAnswer, 0},
		{"llm keeps the model score", Answer{Correct: true, Via: ViaLLM, Score: 87}, Correct, ViaLLM, 87},
		{"llm wrong keeps its score", Answer{Correct: false, Via: ViaLLM, Score: 35}, Wrong, ViaLLM, 35},
		{"self assessment keeps its level", Answer{Correct: true, Via: ViaSelf, Score: 80}, Correct, ViaSelf, 80},
		{"lookup always scores 0", Answer{Correct: false, Via: ViaLookup}, Wrong, ViaLookup, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, err := Apply(New([]int{1}), bank, Enter{ListNo: 1})
			if err != nil {
				t.Fatalf("Enter: %v", err)
			}
			st, err = Apply(st, bank, tc.answer)
			if err != nil {
				t.Fatalf("Answer: %v", err)
			}
			attempts := listOf(t, st, 1).Attempts
			if len(attempts) != 1 {
				t.Fatalf("attempts = %d, want 1", len(attempts))
			}
			got := attempts[0]
			if got.Result != tc.wantResult || got.Via != tc.wantVia || got.Score != tc.wantScore {
				t.Fatalf("attempt = %+v, want result %q via %q score %d", got, tc.wantResult, tc.wantVia, tc.wantScore)
			}
		})
	}
}
