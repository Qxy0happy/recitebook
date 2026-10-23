package engine

import "fmt"

// Result is the outcome of one question-level attempt.
type Result string

const (
	Correct Result = "correct"
	Wrong   Result = "wrong"
)

// Via records how the answer was produced.
type Via string

const (
	ViaAnswer Via = "answer"
	ViaLookup Via = "lookup"
	ViaLLM    Via = "llm"
	ViaSelf   Via = "self"
)

// Question is the engine-facing view of one question.
type Question struct {
	ID    string   `json:"id"`
	Slots []string `json:"slots"`
}

// Bank supplies the question shape the engine needs.
type Bank interface {
	Questions(listNo int) []Question
	Question(listNo int, id string) (Question, bool)
}

// Action is one state transition.
type Action interface{ action() }

// Enter starts the quiz for one list. ListNo 0 means the head of the queue.
type Enter struct{ ListNo int }

// Answer records one question-level result. Score is the 0-100 grade shown
// to the learner; a correct answer without an explicit score counts as 100.
type Answer struct {
	Correct bool
	Via     Via
	Score   int
}

// JumpOut abandons the active quiz. The list moves to the queue tail while it
// still has pending questions, and completes when none remain.
type JumpOut struct{}

// StartRound begins the next round. It is only allowed once every list has
// been attempted in the current round; lists without questions count as done.
type StartRound struct{}

func (Enter) action()      {}
func (Answer) action()     {}
func (JumpOut) action()    {}
func (StartRound) action() {}

// Attempt is one recorded question result.
type Attempt struct {
	Round      int    `json:"round"`
	QuestionID string `json:"question_id"`
	Result     Result `json:"result"`
	Via        Via    `json:"via"`
	Score      int    `json:"score"`
	Seq        int    `json:"seq"`
}

// ListState holds the per-list history for the current round.
type ListState struct {
	ListNo     int       `json:"list_no"`
	Attempts   []Attempt `json:"attempts"`
	Pending    []string  `json:"pending"`
	FirstWrong []string  `json:"first_wrong"`
	Completed  bool      `json:"completed"`
}

// Quiz is the active question set.
type Quiz struct {
	ListNo      int      `json:"list_no"`
	QuestionIDs []string `json:"question_ids"`
	Index       int      `json:"index"`
}

// State is the whole recitation state.
type State struct {
	Round   int         `json:"round"`
	Queue   []int       `json:"queue"`
	Current int         `json:"current"`
	Quiz    *Quiz       `json:"quiz"`
	Lists   []ListState `json:"lists"`
	Seq     int         `json:"seq"`
}

// New builds the first-round state for the given lists.
func New(listNos []int) State {
	st := State{Round: 1, Queue: append([]int(nil), listNos...)}
	for _, no := range listNos {
		st.Lists = append(st.Lists, ListState{ListNo: no})
	}
	return st
}

// Apply returns the next state. The input state is never mutated.
func Apply(st State, bank Bank, action Action) (State, error) {
	st = clone(st)

	switch a := action.(type) {
	case Enter:
		if st.Quiz != nil {
			return st, fmt.Errorf("quiz already in progress")
		}
		listNo := a.ListNo
		if listNo == 0 {
			listNo = defaultList(st)
			if listNo == 0 {
				return st, fmt.Errorf("queue is empty")
			}
		}
		ids := buildQuiz(&st, bank, listNo)
		if len(ids) == 0 {
			idx := listIndex(&st, listNo)
			st.Lists[idx].Completed = true
			st.Queue = remove(st.Queue, listNo)
			st.Current = 0
			st.Quiz = nil
			return st, nil
		}
		st.Current = listNo
		st.Quiz = &Quiz{ListNo: listNo, QuestionIDs: ids}
		return st, nil

	case Answer:
		if st.Quiz == nil {
			return st, fmt.Errorf("no quiz in progress")
		}
		quiz := *st.Quiz
		if quiz.Index >= len(quiz.QuestionIDs) {
			return st, fmt.Errorf("quiz already finished")
		}
		result := Wrong
		if a.Correct {
			result = Correct
		}
		via := a.Via
		if via == "" {
			via = ViaAnswer
		}
		st.Seq++
		idx := listIndex(&st, quiz.ListNo)
		ls := &st.Lists[idx]
		questionID := quiz.QuestionIDs[quiz.Index]
		firstAttempt := !attemptedThisRound(*ls, st.Round, questionID)
		ls.Attempts = append(ls.Attempts, Attempt{
			Round:      st.Round,
			QuestionID: questionID,
			Result:     result,
			Via:        via,
			Score:      normalizeScore(a.Correct, a.Score),
			Seq:        st.Seq,
		})

		if result == Correct {
			ls.Pending = removeString(ls.Pending, questionID)
		} else {
			if !containsString(ls.Pending, questionID) {
				ls.Pending = append(ls.Pending, questionID)
			}
			if firstAttempt && !containsString(ls.FirstWrong, questionID) {
				ls.FirstWrong = append(ls.FirstWrong, questionID)
			}
		}

		quiz.Index++
		if quiz.Index >= len(quiz.QuestionIDs) {
			st.Quiz = nil
			if len(ls.Pending) == 0 {
				ls.Completed = true
				st.Queue = remove(st.Queue, quiz.ListNo)
				st.Current = 0
			} else {
				st.Current = quiz.ListNo
			}
		} else {
			st.Quiz = &quiz
		}
		return st, nil

	case JumpOut:
		if st.Quiz == nil {
			return st, fmt.Errorf("no quiz in progress")
		}
		listNo := st.Quiz.ListNo
		idx := listIndex(&st, listNo)
		st.Quiz = nil
		st.Current = 0
		if len(st.Lists[idx].Pending) == 0 {
			st.Lists[idx].Completed = true
			st.Queue = remove(st.Queue, listNo)
		} else {
			st.Queue = append(remove(st.Queue, listNo), listNo)
		}
		return st, nil

	case StartRound:
		if !CanStartRound(st, bank) {
			return st, fmt.Errorf("round %d still has unattempted lists", st.Round)
		}
		st.Round++
		st.Quiz = nil
		st.Current = 0
		queue := make([]int, 0, len(st.Lists))
		for i := range st.Lists {
			ls := &st.Lists[i]
			ls.Pending = append([]string(nil), ls.FirstWrong...)
			ls.FirstWrong = nil
			ls.Completed = len(ls.Pending) == 0
			if !ls.Completed {
				queue = append(queue, ls.ListNo)
			}
		}
		st.Queue = queue
		return st, nil
	}

	return st, fmt.Errorf("unknown action")
}

// CanStartRound reports whether the current round is over: every list has been
// attempted at least once. Lists without questions and lists that already
// completed this round (they had nothing pending to retest) count as done.
func CanStartRound(st State, bank Bank) bool {
	if len(st.Lists) == 0 {
		return false
	}
	for _, ls := range st.Lists {
		if ls.Completed || attemptedInRound(ls, st.Round) || len(bank.Questions(ls.ListNo)) == 0 {
			continue
		}
		return false
	}
	return true
}

// defaultList picks the list Enter starts when no explicit list is given: the
// current list while it still has pending questions, otherwise the queue head.
func defaultList(st State) int {
	if st.Current != 0 {
		for _, ls := range st.Lists {
			if ls.ListNo == st.Current && !ls.Completed && len(ls.Pending) > 0 {
				return st.Current
			}
		}
	}
	if len(st.Queue) > 0 {
		return st.Queue[0]
	}
	return 0
}

// buildQuiz returns the question set for a list. A list entered for the first
// time in its life gets every question; any later entry (including the first
// entry of a new round, whose pending set is the previous round's first
// wrongs) uses the pending baseline plus enough correct questions to reach
// floor(pending * 1.2), capped by the available pool.
func buildQuiz(st *State, bank Bank, listNo int) []string {
	idx := listIndex(st, listNo)
	ls := &st.Lists[idx]
	questions := bank.Questions(listNo)

	if len(ls.Attempts) == 0 {
		ids := make([]string, len(questions))
		for i, q := range questions {
			ids[i] = q.ID
		}
		return ids
	}
	if len(ls.Pending) == 0 {
		return nil
	}

	pending := make(map[string]bool, len(ls.Pending))
	for _, id := range ls.Pending {
		pending[id] = true
	}
	ids := make([]string, 0, len(questions))
	for _, q := range questions {
		if pending[q.ID] {
			ids = append(ids, q.ID)
		}
	}

	// floor(pending * 1.2) - pending, never negative.
	add := len(ls.Pending)*6/5 - len(ls.Pending)
	if add <= 0 {
		return ids
	}
	correct := supplementResults(*ls, st.Round)
	for _, q := range questions {
		if add == 0 {
			break
		}
		if pending[q.ID] || correct[q.ID] != Correct {
			continue
		}
		ids = append(ids, q.ID)
		add--
	}
	return ids
}

func attemptedInRound(ls ListState, round int) bool {
	for _, a := range ls.Attempts {
		if a.Round == round {
			return true
		}
	}
	return false
}

func attemptedThisRound(ls ListState, round int, questionID string) bool {
	for _, a := range ls.Attempts {
		if a.Round == round && a.QuestionID == questionID {
			return true
		}
	}
	return false
}

// supplementResults returns the result used to pick supplement questions: the
// latest attempt of the current round, or, for questions not answered yet this
// round, the latest attempt of the previous round. Older rounds never leak into
// the pool.
func supplementResults(ls ListState, round int) map[string]Result {
	results := make(map[string]Result, len(ls.Attempts))
	for _, a := range ls.Attempts {
		if a.Round == round-1 {
			results[a.QuestionID] = a.Result
		}
	}
	for _, a := range ls.Attempts {
		if a.Round == round {
			results[a.QuestionID] = a.Result
		}
	}
	return results
}

// normalizeScore clamps a grade to 0-100 and treats a correct answer without an
// explicit score as a full score, so auto-graded callers can omit it.
func normalizeScore(correct bool, score int) int {
	if score == 0 && correct {
		return 100
	}
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

// WordResult returns the last question-level result that touched the word.
func WordResult(st State, bank Bank, listNo int, word string) Result {
	for _, ls := range st.Lists {
		if ls.ListNo != listNo {
			continue
		}
		var last Result
		for _, attempt := range ls.Attempts {
			if attempt.Round != st.Round {
				continue
			}
			q, ok := bank.Question(listNo, attempt.QuestionID)
			if !ok {
				continue
			}
			for _, slot := range q.Slots {
				if slot == word {
					last = attempt.Result
				}
			}
		}
		return last
	}
	return ""
}

func listIndex(st *State, listNo int) int {
	for i := range st.Lists {
		if st.Lists[i].ListNo == listNo {
			return i
		}
	}
	st.Lists = append(st.Lists, ListState{ListNo: listNo})
	return len(st.Lists) - 1
}

func remove(xs []int, x int) []int {
	out := xs[:0]
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func removeString(xs []string, x string) []string {
	out := xs[:0]
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

func clone(st State) State {
	c := st
	c.Queue = append([]int(nil), st.Queue...)
	c.Lists = make([]ListState, len(st.Lists))
	for i, ls := range st.Lists {
		c.Lists[i] = ls
		c.Lists[i].Attempts = append([]Attempt(nil), ls.Attempts...)
		c.Lists[i].Pending = append([]string(nil), ls.Pending...)
		c.Lists[i].FirstWrong = append([]string(nil), ls.FirstWrong...)
	}
	if st.Quiz != nil {
		q := *st.Quiz
		q.QuestionIDs = append([]string(nil), st.Quiz.QuestionIDs...)
		c.Quiz = &q
	}
	return c
}
