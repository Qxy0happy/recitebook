package main

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"recitebook/internal/engine"
	"recitebook/internal/store"
)

// WordState is the latest result for one headword.
type WordState struct {
	Spelling string        `json:"spelling"`
	Result   engine.Result `json:"result"`
}

// EngineService owns the persisted recitation state.
type EngineService struct {
	mu      sync.Mutex
	bank    *BankService
	db      store.Store
	state   engine.State
	warning string
}

// NewEngineService loads the saved state, creating a first round on first run.
func NewEngineService(bank *BankService, db store.Store, warning string) (*EngineService, error) {
	state, err := db.Load()
	switch {
	case errors.Is(err, store.ErrNoState):
		state = engine.New(bank.ListNumbers())
		if err := db.Save(state); err != nil {
			return nil, fmt.Errorf("create first round: %w", err)
		}
	case err != nil:
		return nil, err
	}
	return &EngineService{bank: bank, db: db, state: state, warning: warning}, nil
}

// StartupWarning reports a recovered database problem, empty when none.
func (s *EngineService) StartupWarning() string {
	return s.warning
}

// State returns the current state.
func (s *EngineService) State() engine.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// exportTo writes a backup without exposing io types to Wails bindings.
func (s *EngineService) exportTo(w io.Writer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Export(w)
}

// importFrom validates and replaces the saved progress, then reloads the
// in-memory state so the UI observes the imported data immediately.
func (s *EngineService) importFrom(r io.Reader) (engine.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.db.Import(r); err != nil {
		return s.state, err
	}
	state, err := s.db.Load()
	if err != nil {
		return s.state, fmt.Errorf("读取导入结果失败：%w", err)
	}
	s.state = state
	return s.state, nil
}

// Enter starts a quiz for one list. ListNo 0 uses the queue head.
func (s *EngineService) Enter(listNo int) (engine.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next, err := engine.Apply(s.state, s.bank, engine.Enter{ListNo: listNo})
	if err != nil {
		return s.state, err
	}
	if err := s.db.Save(next); err != nil {
		return s.state, fmt.Errorf("保存进度失败：%w", err)
	}
	s.state = next
	return s.state, nil
}

// Preview marks one list's word cards as skimmed for the current round. It
// starts no quiz and leaves the queue untouched.
func (s *EngineService) Preview(listNo int) (engine.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next, err := engine.Apply(s.state, s.bank, engine.Preview{ListNo: listNo})
	if err != nil {
		return s.state, err
	}
	if err := s.db.Save(next); err != nil {
		return s.state, fmt.Errorf("保存进度失败：%w", err)
	}
	s.state = next
	return s.state, nil
}

// Answer records the result of an auto-graded question (single choice or
// fill-in): a correct answer scores 100, a wrong one 0.
func (s *EngineService) Answer(correct bool) (engine.State, error) {
	score := 0
	if correct {
		score = 100
	}
	return s.answer(engine.Answer{Correct: correct, Via: engine.ViaAnswer, Score: score})
}

// AnswerLLM records a full-sentence translation graded by the model. The score
// is the model's 0-100 grade and is stored with the attempt.
func (s *EngineService) AnswerLLM(correct bool, score int) (engine.State, error) {
	return s.answer(engine.Answer{Correct: correct, Via: engine.ViaLLM, Score: score})
}

// AnswerSelf records a four-level self-assessment result (0 / 50 / 80 / 100)
// when LLM grading is unavailable or the question is a word translation.
func (s *EngineService) AnswerSelf(correct bool, score int) (engine.State, error) {
	return s.answer(engine.Answer{Correct: correct, Via: engine.ViaSelf, Score: score})
}

func (s *EngineService) answer(action engine.Answer) (engine.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state.Quiz == nil {
		return s.state, errors.New("no quiz in progress")
	}
	listNo := s.state.Quiz.ListNo
	next, err := engine.Apply(s.state, s.bank, action)
	if err != nil {
		return s.state, err
	}
	return s.record(next, listNo)
}

// Lookup records a lookup on the current question. The whole question counts as
// wrong with ViaLookup, so every bound word turns wrong and the question enters
// the pending set. Only the active question can be looked up: repeating the
// call, or passing a question that has already advanced, is a no-op, so one
// question can never record a second lookup attempt.
func (s *EngineService) Lookup(questionID string) (engine.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state.Quiz == nil {
		return s.state, errors.New("no quiz in progress")
	}
	quiz := *s.state.Quiz
	if quiz.Index >= len(quiz.QuestionIDs) || quiz.QuestionIDs[quiz.Index] != questionID {
		return s.state, nil
	}
	next, err := engine.Apply(s.state, s.bank, engine.Answer{Correct: false, Via: engine.ViaLookup})
	if err != nil {
		return s.state, err
	}
	return s.record(next, quiz.ListNo)
}

// record appends the newest attempt and saves the round snapshot.
func (s *EngineService) record(next engine.State, listNo int) (engine.State, error) {
	idx := listIndex(next, listNo)
	if idx < 0 || len(next.Lists[idx].Attempts) == 0 {
		return s.state, errors.New("engine recorded no attempt")
	}
	attempt := next.Lists[idx].Attempts[len(next.Lists[idx].Attempts)-1]
	if err := s.db.AppendAttempt(listNo, attempt); err != nil {
		return s.state, fmt.Errorf("保存作答失败：%w", err)
	}
	if err := s.db.Save(next); err != nil {
		return s.state, fmt.Errorf("保存进度失败：%w", err)
	}
	s.state = next
	return s.state, nil
}

// JumpOut abandons the current quiz. The list moves to the queue tail while it
// still has pending questions, and completes when none remain.
func (s *EngineService) JumpOut() (engine.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next, err := engine.Apply(s.state, s.bank, engine.JumpOut{})
	if err != nil {
		return s.state, err
	}
	if err := s.db.Save(next); err != nil {
		return s.state, fmt.Errorf("保存进度失败：%w", err)
	}
	s.state = next
	return s.state, nil
}

// CanStartRound reports whether every list has been attempted this round.
func (s *EngineService) CanStartRound() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return engine.CanStartRound(s.state, s.bank)
}

// StartRound begins the next round, retesting the previous round's first wrong
// questions. It is rejected until every list has been attempted.
func (s *EngineService) StartRound() (engine.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next, err := engine.Apply(s.state, s.bank, engine.StartRound{})
	if err != nil {
		return s.state, err
	}
	if err := s.db.Save(next); err != nil {
		return s.state, fmt.Errorf("保存进度失败：%w", err)
	}
	s.state = next
	return s.state, nil
}

// WordStates returns the latest result for every headword in a list.
func (s *EngineService) WordStates(listNo int) []WordState {
	s.mu.Lock()
	defer s.mu.Unlock()

	detail := s.bank.Detail(listNo)
	out := make([]WordState, 0, len(detail.Words))
	for _, word := range detail.Words {
		out = append(out, WordState{
			Spelling: word.Spelling,
			Result:   engine.WordResult(s.state, s.bank, listNo, word.Spelling),
		})
	}
	return out
}

func listIndex(st engine.State, listNo int) int {
	for i := range st.Lists {
		if st.Lists[i].ListNo == listNo {
			return i
		}
	}
	return -1
}
