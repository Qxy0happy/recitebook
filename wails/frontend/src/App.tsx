import { useEffect, useMemo, useRef, useState } from "react";
import { Events } from "@wailsio/runtime";
import {
  BackupService,
  BankService,
  ChatService,
  EngineService,
  GradeService,
  SyncService,
} from "../bindings/recitebook";
import type {
  AskRequest,
  ChatMessageView,
  GradeResult,
  LLMSettingsView,
  ListDetail,
  ListSummary,
  WebDAVSettingsView,
  Word,
  WordState,
} from "../bindings/recitebook";
import { Result } from "../bindings/recitebook/internal/engine/models";
import type { State } from "../bindings/recitebook/internal/engine/models";

type Phase = "queue" | "preview" | "question" | "feedback" | "summary" | "settings";

type PreviewCard =
  | { kind: "outline"; text: string }
  | { kind: "scene"; title: string; story: string }
  | { kind: "word"; word: Word }
  | { kind: "start"; words: number; questions: number };

type Feedback = {
  correct: boolean;
  answer: string;
  score?: number;
  issues?: string[];
  comment?: string;
  self?: boolean;
  lookup?: boolean;
};

const errorMessage = (err: unknown, fallback: string) => {
  if (err instanceof Error && err.message) return err.message;
  if (typeof err === "string" && err.trim()) return err;
  return fallback;
};

const SYNC_DEFAULT_URL = "https://dav.jianguoyun.com/dav/recitebook/progress.json";

// The one question the panel asks by itself the first time it opens.
const CHAT_OPENING_PROMPT = "请讲解这道题的考点，并指出我回答里的问题。";

type ChatDeltaEvent = { request_id: string; question_id: string; delta: string };
type ChatDoneEvent = { request_id: string; question_id: string; message: string };
type ChatErrorEvent = { request_id: string; question_id: string; error: string };

// Stale events are dropped by comparing both identifiers: the question guards
// against a switch mid-answer, the request against an older turn.
const chatEventMatches = (
  data: unknown,
  questionId: string | null,
  requestId: string | null,
) => {
  if (!data || typeof data !== "object" || !questionId || !requestId) return false;
  const payload = data as { request_id?: unknown; question_id?: unknown };
  return payload.question_id === questionId && payload.request_id === requestId;
};

const draftFromSyncView = (view: WebDAVSettingsView | null) => ({
  url: view?.url || SYNC_DEFAULT_URL,
  username: view?.username ?? "",
  password: "",
});


const formatSyncTime = (value: string) => {
  const at = new Date(value);
  if (Number.isNaN(at.getTime())) return value;
  const pad = (part: number) => String(part).padStart(2, "0");
  return `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())} ${pad(at.getHours())}:${pad(at.getMinutes())}`;
};

// The Go service already reports HTTP status codes; translate the three cases
// the user can act on and pass everything else through untouched.
const syncErrorMessage = (err: unknown, direction: "upload" | "download") => {
  const raw = errorMessage(
    err,
    direction === "upload" ? "上传失败，请重试。" : "下载失败，本机进度未改动。",
  );
  const status = raw.match(/HTTP (\d{3})/)?.[1] ?? null;
  if (status === "401" || status === "403")
    return `坚果云认证失败（HTTP ${status}）：请检查账号邮箱与第三方应用密码。`;
  if (status === "404" || status === "409")
    return `云端目录不存在（HTTP ${status}）：请先在坚果云网页端创建同步文件夹。`;
  if (raw.includes("还没有同步文件")) return "云端还没有同步文件，请先上传本机进度。";
  return raw;
};

const SELF_LEVELS: { label: string; correct: boolean; score: number }[] = [
  { label: "完全不会", correct: false, score: 0 },
  { label: "只译出个别词", correct: false, score: 50 },
  { label: "基本达意", correct: true, score: 80 },
  { label: "准确通顺", correct: true, score: 100 },
];

function PreviewCardView({ card, reviewing = false }: { card: PreviewCard; reviewing?: boolean }) {
  if (card.kind === "outline") {
    return (
      <>
        <p className="preview-kind">类目联想</p>
        <p className="preview-body preview-outline">{card.text}</p>
      </>
    );
  }
  if (card.kind === "scene") {
    return (
      <>
        <p className="preview-kind">场景</p>
        <h2 className="preview-title">{card.title}</h2>
        <p className="preview-body">{card.story}</p>
      </>
    );
  }
  if (card.kind === "word") {
    const word = card.word;
    return (
      <>
        <p className="preview-kind">单词</p>
        <h2 className="preview-title">
          <span className="word-spelling">{word.spelling}</span>
          {word.ipa && <span className="word-ipa">{word.ipa}</span>}
        </h2>
        <div className="preview-body">
          {(word.senses ?? []).map((sense) => (
            <p key={`${sense.pos}-${sense.meaning_cn}`}>
              <span className="word-pos">{sense.pos}</span> {sense.meaning_cn}
            </p>
          ))}
          {word.mnemonic && <p className="word-mnemonic">妙记：{word.mnemonic}</p>}
          {(word.notes ?? []).map((note) => (
            <p key={`${note.marker}-${note.label_raw}`} className="word-note">
              {note.marker} {note.label_raw}：{note.body}
            </p>
          ))}
        </div>
      </>
    );
  }
  return (
    <>
      <p className="preview-kind">{reviewing ? "本轮已清" : "准备开始"}</p>
      <p className="preview-body">
        {reviewing ? (
          <>
            共 <span className="num">{card.words}</span> 个词。只读复习，不再出题。
          </>
        ) : (
          <>
            共 <span className="num">{card.words}</span> 个词，
            <span className="num">{card.questions}</span> 道题。答完一题立刻看到对错与解析。
          </>
        )}
      </p>
    </>
  );
}

export default function App() {
  const [lists, setLists] = useState<ListSummary[]>([]);
  const [state, setState] = useState<State | null>(null);
  const [detail, setDetail] = useState<ListDetail | null>(null);
  const [phase, setPhase] = useState<Phase>("queue");
  const [activeId, setActiveId] = useState<string | null>(null);
  const [step, setStep] = useState(0);
  const [picked, setPicked] = useState<number | null>(null);
  const [text, setText] = useState("");
  const [blanks, setBlanks] = useState<string[]>([]);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [words, setWords] = useState<WordState[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [flash, setFlash] = useState<string | null>(null);
  const [quizTotal, setQuizTotal] = useState(0);
  const [settings, setSettings] = useState<LLMSettingsView | null>(null);
  const [draft, setDraft] = useState({ baseUrl: "", model: "", apiKey: "" });
  const [settingsSaved, setSettingsSaved] = useState(false);
  const [grading, setGrading] = useState(false);
  const [selfAssess, setSelfAssess] = useState(false);
  const [beforeSettings, setBeforeSettings] = useState<Phase>("queue");
  const [cardsOpen, setCardsOpen] = useState(false);
  const [previewIndex, setPreviewIndex] = useState(0);
  const [reviewing, setReviewing] = useState(false);
  const [openWord, setOpenWord] = useState<string | null>(null);
  const [backupOpen, setBackupOpen] = useState(false);
  const [backupBusy, setBackupBusy] = useState(false);
  const [backupNote, setBackupNote] = useState<string | null>(null);
  const [syncSettings, setSyncSettings] = useState<WebDAVSettingsView | null>(null);
  const [syncDraft, setSyncDraft] = useState({
    url: SYNC_DEFAULT_URL,
    username: "",
    password: "",
  });
  const [syncSaved, setSyncSaved] = useState(false);
  const [syncBusy, setSyncBusy] = useState<"save" | "upload" | "download" | null>(null);
  const [syncConfirm, setSyncConfirm] = useState<"upload" | "download" | null>(null);
  const [syncNote, setSyncNote] = useState<string | null>(null);
  const [lastSyncedAt, setLastSyncedAt] = useState("");
  const [canStartRound, setCanStartRound] = useState(false);
  const [chatOpen, setChatOpen] = useState(false);
  const [chatMessages, setChatMessages] = useState<ChatMessageView[]>([]);
  const [chatDraft, setChatDraft] = useState("");
  const [chatStream, setChatStream] = useState("");
  const [chatBusy, setChatBusy] = useState(false);
  const [chatError, setChatError] = useState<string | null>(null);

  // The chat event listeners mount once. Refs carry what they compare against,
  // so a stale closure from an earlier render never touches the next question.
  const chatRequest = useRef<string | null>(null);
  const chatPending = useRef<string | null>(null);
  const chatMessagesRef = useRef<ChatMessageView[]>([]);
  const chatBusyRef = useRef(false);
  const chatLoaded = useRef(false);
  const chatAutoAsk = useRef<string | null>(null);
  const chatQuestion = useRef<string | null>(null);
  const chatSend = useRef<(prompt: string) => void>(() => {});

  useEffect(() => {
    Promise.all([
      BankService.Lists(),
      EngineService.State(),
      EngineService.StartupWarning(),
      GradeService.Settings().catch(() => null),
      SyncService.Settings().catch(() => null),
    ])
      .then(async ([rows, loaded, warning, saved, webdav]) => {
        setLists(rows ?? []);
        setState(loaded);
        if (warning) setError(warning);
        if (saved) {
          setSettings(saved);
          setDraft({ baseUrl: saved.base_url, model: saved.model, apiKey: "" });
        }
        if (webdav) {
          setSyncSettings(webdav);
          setLastSyncedAt(webdav.last_synced_at);
          setSyncDraft(draftFromSyncView(webdav));
        }
        if (loaded.quiz) {
          const active = await BankService.Detail(loaded.quiz.list_no);
          setDetail(active);
          setActiveId(loaded.quiz.question_ids?.[loaded.quiz.index] ?? null);
          setStep(loaded.quiz.index);
          setQuizTotal(loaded.quiz.question_ids?.length ?? 0);
          setPhase("question");
        }
      })
      .catch(() => setError("题库读取失败，请重新打开应用。"));
  }, []);

  useEffect(() => {
    if (!state) return;
    let cancelled = false;
    EngineService.CanStartRound()
      .then((value) => {
        if (!cancelled) setCanStartRound(value);
      })
      .catch(() => {
        if (!cancelled) setCanStartRound(false);
      });
    return () => {
      cancelled = true;
    };
  }, [state]);

  const question = useMemo(() => {
    if (!detail?.questions || !activeId) return null;
    return detail.questions.find((item) => item.id === activeId) ?? null;
  }, [detail, activeId]);

  const previewCards = useMemo<PreviewCard[]>(() => {
    if (!detail) return [];
    const cards: PreviewCard[] = [];
    const outline = detail.association?.outline?.trim();
    if (outline) cards.push({ kind: "outline", text: outline });
    for (const scene of detail.association?.scenes ?? []) {
      cards.push({ kind: "scene", title: scene.title, story: scene.story });
    }
    for (const word of detail.words ?? []) cards.push({ kind: "word", word });
    cards.push({
      kind: "start",
      words: detail.words?.length ?? 0,
      questions: detail.questions?.length ?? 0,
    });
    return cards;
  }, [detail]);

  const isFill = question?.kind === "fill";
  const fillAnswers = question?.answers ?? [];
  const isMultiFill =
    isFill &&
    (question?.slots?.length ?? 0) > 1 &&
    fillAnswers.length === (question?.slots?.length ?? 0);
  const isTranslateWord = question?.kind === "translate_word";
  const isTranslateSentence = question?.kind === "translate_sentence";
  const isTranslate = isTranslateSentence || isTranslateWord;
  const needsText = isFill || isTranslate;
  const canSubmit = isMultiFill
    ? fillAnswers.every((_, index) => (blanks[index] ?? "").trim().length > 0)
    : needsText
      ? text.trim().length > 0
      : picked !== null;
  const reference = question?.answers?.[0] ?? "";

  const chatQuestionId = phase === "feedback" ? (question?.id ?? null) : null;

  const applyChatMessages = (messages: ChatMessageView[]) => {
    chatMessagesRef.current = messages;
    setChatMessages(messages);
  };

  const markChatBusy = (value: boolean) => {
    chatBusyRef.current = value;
    setChatBusy(value);
  };

  // What the learner actually answered, in the same terms the feedback screen
  // shows. A lookup-triggered feedback has no answer to report.
  const chatUserAnswer = () => {
    if (!question || !feedback || feedback.lookup) return "";
    if (isFill) return isMultiFill ? blanks.join(" / ") : text;
    if (isTranslate) return text;
    return picked !== null ? (question.options?.[picked] ?? "") : "";
  };

  const chatOutcome = () => {
    if (!feedback) return "";
    const verdict = feedback.lookup
      ? "查词判错"
      : feedback.self
        ? "自评"
        : feedback.correct
          ? "答对"
          : "答错";
    const parts = [verdict];
    if (typeof feedback.score === "number") parts.push(`${feedback.score}/100`);
    const issues = (feedback.issues ?? []).map((issue) => issue.trim()).filter(Boolean);
    if (issues.length > 0) parts.push(`扣分点：${issues.join("；")}`);
    return parts.join("，");
  };

  // One failure path for both the chat:error event and the promise rejection.
  // The trailing optimistic user bubble is withdrawn so a retry does not leave
  // a duplicate behind; on a save-only failure the last bubble is the reply and
  // stays put.
  const failChat = (requestId: string, message: string) => {
    if (chatRequest.current !== requestId) return;
    const messages = chatMessagesRef.current;
    const last = messages[messages.length - 1];
    const pending = chatPending.current;
    if (last && last.role === "user" && pending !== null && last.content === pending) {
      applyChatMessages(messages.slice(0, -1));
      setChatDraft(pending);
    }
    chatPending.current = null;
    chatRequest.current = null;
    setChatError(message);
    setChatStream("");
    markChatBusy(false);
  };

  const sendChat = (raw: string) => {
    const prompt = raw.trim();
    if (!prompt || chatBusyRef.current) return;
    if (phase !== "feedback" || !question) return;

    const requestId = crypto.randomUUID();
    chatRequest.current = requestId;
    chatPending.current = prompt;
    markChatBusy(true);
    applyChatMessages([...chatMessagesRef.current, { role: "user", content: prompt }]);
    setChatDraft("");
    setChatError(null);
    setChatStream("");

    const req: AskRequest = {
      list_no: detail?.list_no ?? 0,
      question_id: question.id,
      user_answer: chatUserAnswer(),
      outcome: chatOutcome(),
      prompt,
      request_id: requestId,
    };
    // chat:error carries every mid-stream failure. A rejection without one only
    // happens before streaming starts (bad arguments, an in-flight request).
    ChatService.Ask(req).catch((err: unknown) => {
      failChat(requestId, errorMessage(err, "对话失败，请重试。"));
    });
  };

  useEffect(() => {
    chatSend.current = sendChat;
  });

  useEffect(() => {
    applyChatMessages([]);
    setChatDraft("");
    setChatStream("");
    markChatBusy(false);
    setChatError(null);
    setChatOpen(false);
    chatRequest.current = null;
    chatPending.current = null;
    chatLoaded.current = false;
    chatAutoAsk.current = null;
    chatQuestion.current = chatQuestionId;
    if (!chatQuestionId) return;

    // A stale answer from the previous question is dropped here, not written
    // into the next one's panel.
    let cancelled = false;
    ChatService.History(chatQuestionId)
      .then((saved) => {
        if (!cancelled) applyChatMessages(saved ?? []);
      })
      .catch(() => {
        if (!cancelled) applyChatMessages([]);
      })
      .finally(() => {
        if (cancelled) return;
        chatLoaded.current = true;
        // The panel was opened before the saved conversation arrived; ask the
        // opening question only if there turned out to be nothing to resume.
        if (chatAutoAsk.current !== chatQuestionId || chatBusyRef.current) return;
        if (chatMessagesRef.current.length > 0) return;
        chatAutoAsk.current = null;
        chatSend.current(CHAT_OPENING_PROMPT);
      });
    return () => {
      cancelled = true;
    };
    // applyChatMessages and markChatBusy only touch refs and stable setters.
  }, [chatQuestionId]);

  useEffect(() => {
    const offDelta = Events.On("chat:delta", (ev) => {
      if (!chatEventMatches(ev.data, chatQuestion.current, chatRequest.current)) return;
      if (!chatBusyRef.current) return;
      const { delta } = ev.data as ChatDeltaEvent;
      if (delta) setChatStream((value) => value + delta);
    });

    const offDone = Events.On("chat:done", (ev) => {
      if (!chatEventMatches(ev.data, chatQuestion.current, chatRequest.current)) return;
      const { message } = ev.data as ChatDoneEvent;
      applyChatMessages([...chatMessagesRef.current, { role: "assistant", content: message }]);
      chatPending.current = null;
      setChatStream("");
      markChatBusy(false);
    });

    const offError = Events.On("chat:error", (ev) => {
      if (!chatEventMatches(ev.data, chatQuestion.current, chatRequest.current)) return;
      const { error } = ev.data as ChatErrorEvent;
      failChat(chatRequest.current ?? "", error || "对话失败，请重试。");
    });

    return () => {
      offDelta();
      offDone();
      offError();
    };
    // Registered once; the handlers read refs instead of captured state.
  }, []);

  const openChat = () => {
    if (chatOpen) {
      setChatOpen(false);
      // Closing before the saved conversation lands must cancel the deferred
      // first question instead of spending tokens into a hidden panel.
      chatAutoAsk.current = null;
      return;
    }
    setChatOpen(true);
    if (chatBusyRef.current || chatMessagesRef.current.length > 0) return;
    // A saved conversation must land before the panel asks by itself, or the
    // first turn would be posted twice.
    if (!chatLoaded.current) {
      chatAutoAsk.current = question?.id ?? null;
      return;
    }
    chatSend.current(CHAT_OPENING_PROMPT);
  };

  const pendingOf = (listNo: number) =>
    state?.lists?.find((item) => item.list_no === listNo)?.pending?.length ?? 0;

  const startQuiz = async (listNo: number) => {
    try {
      setError(null);
      setFlash(null);
      const next = await EngineService.Enter(listNo);
      setState(next);
      if (!next.quiz) {
        setDetail(null);
        setActiveId(null);
        setQuizTotal(0);
        setFeedback(null);
        setPhase("queue");
        setFlash(`List ${listNo} 本轮没有更多题目。`);
        return;
      }
      const loaded = await BankService.Detail(listNo);
      setDetail(loaded);
      const index = next.quiz.index ?? 0;
      setActiveId(next.quiz.question_ids?.[index] ?? null);
      setStep(index);
      setQuizTotal(next.quiz.question_ids?.length ?? 0);
      setPicked(null);
      setText("");
      setBlanks([]);
      setFeedback(null);
      setSelfAssess(false);
      setCardsOpen(false);
      setOpenWord(null);
      setReviewing(false);
      setPhase("question");
    } catch {
      setError("这个 List 的题目还没有导入。");
    }
  };

  // A cleared list opens read-only cards and a summary. A suspended group
  // resumes directly. Only a list that has never been previewed this round
  // shows the word cards before its first question.
  const enter = async (listNo: number) => {
    const row = state?.lists?.find((item) => item.list_no === listNo);
    if (row?.completed) {
      await review(listNo);
      return;
    }
    if (row && (row.group?.length ?? 0) > 0) {
      await startQuiz(listNo);
      return;
    }
    if (row && !row.previewed) {
      try {
        const loaded = await BankService.Detail(listNo);
        if ((loaded.questions?.length ?? 0) > 0) {
          setError(null);
          setFlash(null);
          const next = await EngineService.Preview(listNo);
          setState(next);
          setDetail(loaded);
          setActiveId(null);
          setQuizTotal(0);
          setPicked(null);
          setText("");
          setBlanks([]);
          setFeedback(null);
          setPreviewIndex(0);
          setCardsOpen(false);
          setOpenWord(null);
          setReviewing(false);
          setPhase("preview");
          return;
        }
      } catch {
        // Fall through so the quiz path reports the missing bank data.
      }
    }
    await startQuiz(listNo);
  };

  const review = async (listNo: number) => {
    try {
      setError(null);
      setFlash(null);
      const loaded = await BankService.Detail(listNo);
      const result = await EngineService.WordStates(listNo);
      setDetail(loaded);
      setWords(result ?? []);
      setActiveId(null);
      setStep(0);
      setQuizTotal(0);
      setPicked(null);
      setText("");
      setBlanks([]);
      setFeedback(null);
      setSelfAssess(false);
      setPreviewIndex(0);
      setCardsOpen(false);
      setOpenWord(null);
      setReviewing(true);
      setPhase("preview");
    } catch {
      setError("这个 List 的词卡读取失败。");
    }
  };

  const leavePreview = () => {
    setDetail(null);
    setActiveId(null);
    setPreviewIndex(0);
    setFeedback(null);
    setReviewing(false);
    setPhase("queue");
  };

  const jumpOut = async () => {
    if (!state?.quiz) return;
    const listNo = state.quiz.list_no;
    const remaining =
      (state.quiz.question_ids?.length ?? 0) - (state.quiz.index ?? 0);
    try {
      const next = await EngineService.JumpOut();
      setState(next);
      setDetail(null);
      setActiveId(null);
      setPicked(null);
      setText("");
      setBlanks([]);
      setFeedback(null);
      setQuizTotal(0);
      setCardsOpen(false);
      setOpenWord(null);
      setReviewing(false);
      setPhase("queue");
      setFlash(
        remaining > 0
          ? `已暂停 List ${listNo}，本组还剩 ${remaining} 题未作答，已放回队尾。`
          : `已暂停 List ${listNo}，已放回队尾。`,
      );
    } catch {
      setError("跳出失败，请重试。");
    }
  };

  const startRound = async () => {
    try {
      setError(null);
      const next = await EngineService.StartRound();
      setState(next);
      setDetail(null);
      setActiveId(null);
      setPicked(null);
      setText("");
      setBlanks([]);
      setFeedback(null);
      setQuizTotal(0);
      setCardsOpen(false);
      setOpenWord(null);
      setReviewing(false);
      setPhase("queue");
      setFlash(`已进入第 ${next.round} 轮，只重考上一轮的错题。`);
    } catch {
      setError("还有 List 本轮未作答，暂不能开启新一轮。");
    }
  };

  const submit = async () => {
    if (!question || !canSubmit || grading) return;

    // Word translation is graded by the learner on the four-level scale; it
    // never calls the LLM grader.
    if (isTranslateWord) {
      setSelfAssess(true);
      return;
    }

    if (isTranslateSentence) {
      setGrading(true);
      setSelfAssess(false);
      let grade: GradeResult;
      try {
        grade = await GradeService.Grade(question.id, question.stem, text.trim(), reference);
      } catch {
        setGrading(false);
        setSelfAssess(true);
        return;
      }
      setGrading(false);
      try {
        const next = await EngineService.AnswerLLM(grade.correct, grade.score);
        setState(next);
        setFeedback({
          correct: grade.correct,
          answer: reference,
          score: grade.score,
          issues: grade.issues ?? [],
          comment: grade.comment ?? "",
        });
        setPhase("feedback");
      } catch {
        setError("提交失败，请重试。");
      }
      return;
    }

    const correct = isFill
      ? isMultiFill
        ? fillAnswers.every(
            (answer, index) =>
              answer.trim().toLowerCase() === (blanks[index] ?? "").trim().toLowerCase(),
          )
        : (question.answers ?? []).some(
            (item) => item.trim().toLowerCase() === text.trim().toLowerCase(),
          )
      : picked !== null && (picked === question.answer || (question.accept ?? []).includes(picked));
    try {
      const next = await EngineService.Answer(correct);
      setState(next);
      setFeedback({
        correct,
        score: correct ? 100 : 0,
        answer: isFill
          ? isMultiFill
            ? fillAnswers.join(" / ")
            : (question.answers?.[0] ?? "")
          : (question.options?.[question.answer] ?? ""),
      });
      setPhase("feedback");
    } catch {
      setError("提交失败，请重试。");
    }
  };

  const assess = async (level: { correct: boolean; score: number }) => {
    if (!question) return;
    try {
      const next = await EngineService.AnswerSelf(level.correct, level.score);
      setState(next);
      setFeedback({ correct: level.correct, answer: reference, self: true, score: level.score });
      setPhase("feedback");
    } catch {
      setError("提交失败，请重试。");
    }
  };

  const toggleSettings = () => {
    if (phase === "settings") {
      setPhase(beforeSettings);
      return;
    }
    setBackupOpen(false);
    setSyncConfirm(null);
    setSettingsSaved(false);
    setBeforeSettings(phase);
    setPhase("settings");
  };

  const saveSettings = async () => {
    setSettingsSaved(false);
    try {
      const next = await GradeService.SaveSettings(draft.baseUrl, draft.model, draft.apiKey);
      setSettings(next);
      setDraft({ baseUrl: next.base_url, model: next.model, apiKey: "" });
      setSettingsSaved(true);
      setError(null);
    } catch {
      setError("保存设置失败。");
    }
  };

  const lookup = async (spelling: string) => {
    if (!question) return;
    setOpenWord((value) => (value === spelling ? null : spelling));
    if (phase !== "question") return;
    try {
      const next = await EngineService.Lookup(question.id);
      setState(next);
      setFeedback({
        correct: false,
        score: 0,
        answer: isFill
          ? isMultiFill
            ? fillAnswers.join(" / ")
            : (question.answers?.[0] ?? "")
          : (question.options?.[question.answer] ?? ""),
        lookup: true,
      });
      setPhase("feedback");
    } catch {
      setError("查词失败，请重试。");
    }
  };

  const exportBackup = async () => {
    setBackupBusy(true);
    setError(null);
    setBackupNote(null);
    try {
      const result = await BackupService.ExportFile();
      if (!result.canceled) setBackupNote(`已导出到 ${result.path}`);
    } catch (err) {
      setError(errorMessage(err, "导出失败，请重试。"));
    } finally {
      setBackupBusy(false);
    }
  };

  // Shared by 导入备份 and 从云端下载: swap the in-memory view over to the
  // restored snapshot and reopen the quiz it points at. Returns the bank error
  // text when the restored list cannot be loaded.
  const applyRestoredProgress = async (next: State) => {
    setState(next);
    setReviewing(false);
    setWords([]);
    setFeedback(null);
    setPicked(null);
    setText("");
    setBlanks([]);
    setSelfAssess(false);
    if (next.quiz) {
      try {
        const loaded = await BankService.Detail(next.quiz.list_no);
        setDetail(loaded);
        setActiveId(next.quiz.question_ids?.[next.quiz.index] ?? null);
        setStep(next.quiz.index);
        setQuizTotal(next.quiz.question_ids?.length ?? 0);
        setPhase("question");
        return null;
      } catch {
        setDetail(null);
        setActiveId(null);
        setStep(0);
        setQuizTotal(0);
        setPhase("queue");
        return "题库读取失败，请重新进入 List。";
      }
    }
    setDetail(null);
    setActiveId(null);
    setStep(0);
    setQuizTotal(0);
    setPhase("queue");
    return null;
  };

  const importBackup = async () => {
    setBackupBusy(true);
    setError(null);
    setBackupNote(null);
    try {
      const result = await BackupService.ImportFile();
      if (result.canceled) return;
      const next = result.state;
      if (!next) throw new Error("导入失败：备份中没有进度数据。");
      const bankProblem = await applyRestoredProgress(next);
      setBackupNote("导入完成，进度已恢复。");
      if (bankProblem) setError(`导入完成，但${bankProblem}`);
    } catch (err) {
      setError(errorMessage(err, "导入失败，原有进度未改动。"));
    } finally {
      setBackupBusy(false);
    }
  };

  const saveSyncSettings = async () => {
    setSyncBusy("save");
    setSyncSaved(false);
    setSyncNote(null);
    setError(null);
    try {
      const next = await SyncService.SaveSettings(
        syncDraft.url,
        syncDraft.username,
        syncDraft.password,
      );
      setSyncSettings(next);
      setLastSyncedAt(next.last_synced_at);
      setSyncDraft(draftFromSyncView(next));
      setSyncSaved(true);
    } catch (err) {
      setError(errorMessage(err, "保存同步配置失败。"));
    } finally {
      setSyncBusy(null);
    }
  };

  const runSync = async (direction: "upload" | "download") => {
    setSyncConfirm(null);
    setSyncBusy(direction);
    setSyncNote(null);
    setError(null);
    try {
      const result =
        direction === "upload" ? await SyncService.Upload() : await SyncService.Download();
      setLastSyncedAt(result.synced_at);
      if (direction === "upload") {
        setSyncNote(`已上传到云端 · ${formatSyncTime(result.synced_at)}`);
        return;
      }
      const next = await EngineService.State();
      const bankProblem = await applyRestoredProgress(next);
      if (bankProblem) {
        setError(`已从云端下载，但${bankProblem}`);
        return;
      }
      setSyncNote(`已从云端下载 · ${formatSyncTime(result.synced_at)}`);
      setFlash("已从云端下载，进度已恢复。");
    } catch (err) {
      setError(syncErrorMessage(err, direction));
    } finally {
      setSyncBusy(null);
    }
  };

  const advance = async () => {
    if (!state || !detail) return;
    if (!state.quiz) {
      const result = await EngineService.WordStates(detail.list_no);
      setWords(result ?? []);
      setCardsOpen(false);
      setOpenWord(null);
      setPhase("summary");
      return;
    }
    setActiveId(state.quiz.question_ids?.[state.quiz.index] ?? null);
    setStep((value) => value + 1);
    setPicked(null);
    setText("");
    setBlanks([]);
    setFeedback(null);
    setSelfAssess(false);
    setCardsOpen(false);
    setOpenWord(null);
    setPhase("question");
  };

  const statusOf = (listNo: number) => {
    if (state?.quiz?.list_no === listNo) return "进行中";
    const row = state?.lists?.find((item) => item.list_no === listNo);
    if (row?.completed) return "已清";
    if ((row?.group?.length ?? 0) > 0) return "进行中";
    const pending = row?.pending?.length ?? 0;
    return pending > 0 ? "待清" : "未开始";
  };

  const round = state?.round ?? 1;
  const listStates = state?.lists ?? [];
  const activeQuizList = state?.quiz?.list_no ?? 0;
  const cleared = listStates.filter((item) => item.completed).length;
  const pendingTotal = listStates.reduce((sum, item) => sum + (item.pending?.length ?? 0), 0);
  const firstWrongTotal = listStates.reduce(
    (sum, item) => sum + (item.first_wrong?.length ?? 0),
    0,
  );
  const activeLists = listStates.filter(
    (item) =>
      !item.completed && (item.list_no === activeQuizList || (item.group?.length ?? 0) > 0),
  ).length;
  const pendingLists = listStates.filter(
    (item) =>
      !item.completed &&
      item.list_no !== activeQuizList &&
      (item.group?.length ?? 0) === 0 &&
      (item.pending?.length ?? 0) > 0,
  ).length;
  const idleLists = Math.max(0, lists.length - cleared - activeLists - pendingLists);
  const toneOf: Record<string, "done" | "active" | "pending" | "idle"> = {
    已清: "done",
    进行中: "active",
    待清: "pending",
    未开始: "idle",
  };

  const nextNo = state?.current
    ? state.current
    : (state?.queue?.[0] ?? lists.find((row) => statusOf(row.list_no) !== "已清")?.list_no);
  const nextList = nextNo ? (lists.find((row) => row.list_no === nextNo) ?? null) : null;
  const nextPending = pendingOf(nextNo ?? 0);
  const nextFirstWrong =
    state?.lists?.find((item) => item.list_no === nextNo)?.first_wrong?.length ?? 0;
  const quizPending = pendingOf(state?.quiz?.list_no ?? detail?.list_no ?? 0);
  const summaryPending = pendingOf(detail?.list_no ?? 0);
  const gradingLabel = isTranslateSentence
    ? selfAssess || !settings?.api_key_set
      ? "自评给分"
      : "LLM 判分"
    : isTranslateWord
      ? "自评给分"
      : "自动判分";
  const nextListNo = state?.queue?.[0] ?? 0;
  const nextPosition = nextNo ? (state?.queue?.indexOf(nextNo) ?? -1) + 1 : 0;
  const wordCorrect = words.filter((word) => word.result === Result.Correct).length;
  const wordWrong = words.filter((word) => word.result === Result.Wrong).length;
  const wordUntested = words.length - wordCorrect - wordWrong;

  // A cleared list with questions still queued moves on by itself. Going back
  // to the queue (or any other phase change) cancels the pending step. Review
  // is read-only, so it never advances on its own.
  useEffect(() => {
    if (reviewing || phase !== "summary" || summaryPending > 0 || nextListNo === 0) return;
    const timer = setTimeout(() => void enter(nextListNo), 1600);
    return () => clearTimeout(timer);
  }, [phase, summaryPending, nextListNo, reviewing]);
  const queueRows = (state?.queue?.length ? state.queue : lists.map((row) => row.list_no))
    .map((listNo) => lists.find((row) => row.list_no === listNo))
    .filter((row): row is ListSummary => Boolean(row));

  const sealRef = useRef<HTMLElement | null>(null);

  // On phones the seal margin is a horizontal rail; keep the current List's
  // ring in view so entering List 12 does not leave it off-screen.
  useEffect(() => {
    if (!nextNo) return;
    if (!window.matchMedia("(max-width: 640px)").matches) return;
    const item = sealRef.current?.querySelector(".seal-item.is-next");
    item?.scrollIntoView({ inline: "center", block: "nearest" });
  }, [nextNo]);

  return (
    <div className={phase === "question" || phase === "feedback" ? "app app-narrow" : "app"}>
      <div className="sheet">
        <nav className="seal" aria-label="List 索引" ref={sealRef}>
          <ol className="seal-list">
            {lists.map((row) => {
              const status = statusOf(row.list_no);
              const tone = toneOf[status] ?? "idle";
              const isNext = row.list_no === nextNo;
              return (
                <li key={row.list_no} className={`seal-item seal-${tone}${isNext ? " is-next" : ""}`}>
                  <button
                    className="seal-link"
                    onClick={() => enter(row.list_no)}
                    aria-current={isNext ? "step" : undefined}
                    aria-label={`List ${row.list_no} ${row.title}，${status}，待清 ${pendingOf(row.list_no)} 题`}
                  >
                    <span className="seal-hole" aria-hidden="true" />
                    <span className="seal-no num" aria-hidden="true">
                      {String(row.list_no).padStart(2, "0")}
                    </span>
                  </button>
                </li>
              );
            })}
          </ol>
          <p className="seal-label" aria-hidden="true">
            密封线
          </p>
        </nav>

        <div className="page">
          <header className="page-head">
            <div className="brand">
              <svg className="brand-mark" viewBox="0 0 20 20" aria-hidden="true">
                <circle
                  cx="10"
                  cy="10"
                  r="7"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="1.6"
                  strokeDasharray="8.5 6.16"
                  strokeLinecap="round"
                  transform="rotate(-90 10 10)"
                />
              </svg>
              <span className="brand-name">循环背诵</span>
              <span className="brand-meta num">考点词 · {lists.length} 个 List</span>
              <span className="brand-round num">第 {round} 轮</span>
            </div>
            <div className="page-head-actions">
              {phase !== "settings" && (
                <button
                  className="text-action"
                  onClick={() => setBackupOpen((value) => !value)}
                  aria-expanded={backupOpen}
                  aria-controls="backup-bar"
                >
                  备份
                </button>
              )}
              <button className="text-action" onClick={toggleSettings}>
                {phase === "settings" ? "返回" : "设置"}
              </button>
            </div>
          </header>

          <main className="page-body">
            {backupOpen && (
              <section className="backup" id="backup-bar" aria-label="备份与恢复">
                <div className="backup-copy">
                  <p className="backup-title">数据备份</p>
                  <p className="backup-note">
                    导出包含轮次、逐题结果、待清与首错快照和设置，不含 API 密钥。
                  </p>
                  {backupNote && (
                    <p className="note-ok" role="status">
                      {backupNote}
                    </p>
                  )}
                </div>
                <div className="backup-actions">
                  <button className="btn btn-quiet" onClick={exportBackup} disabled={backupBusy}>
                    导出备份
                  </button>
                  <button className="btn btn-quiet" onClick={importBackup} disabled={backupBusy}>
                    导入备份
                  </button>
                  <button className="text-action" onClick={() => setBackupOpen(false)}>
                    关闭
                  </button>
                </div>
              </section>
            )}

            {error && (
              <p className="notice notice-bad" role="alert">
                {error}
              </p>
            )}

            {flash && (
              <p className="notice" role="status">
                {flash}
              </p>
            )}

            {phase === "preview" && (
              <header className="screen-head head-split">
                <div>
                  <h1>{detail?.title ?? "准备中"}</h1>
                  <p className="screen-meta num">
                    List {detail?.list_no ?? "—"} · 第 {round} 轮 · {reviewing ? "复习" : "略览"}{" "}
                    第 {previewIndex + 1} / {previewCards.length} 张
                  </p>
                </div>
                <div className="head-actions">
                  <button className="text-action" onClick={leavePreview}>
                    返回队列
                  </button>
                  {!reviewing && detail && (
                    <button className="text-action" onClick={() => void startQuiz(detail.list_no)}>
                      跳过略览
                    </button>
                  )}
                </div>
              </header>
            )}

            {phase === "settings" && (
              <header className="screen-head">
                <h1>设置</h1>
                <p className="screen-meta">判分接口、本地备份与 WebDAV 同步。</p>
              </header>
            )}

            {phase === "summary" && (
              <header className="screen-head">
                <h1>{detail?.title ?? "准备中"}</h1>
                <p className="screen-meta num">
                  List {detail?.list_no ?? "—"} · 第 {round} 轮 · 本轮 {words.length} 个词
                </p>
              </header>
            )}

            {phase === "preview" && detail && previewCards[previewIndex] && (
              <section
                className="preview"
                aria-label={reviewing ? "本 List 词卡复习" : "本 List 词卡略览"}
              >
                <div className="preview-card">
                  <PreviewCardView card={previewCards[previewIndex]} reviewing={reviewing} />
                </div>

                <div className="preview-nav">
                  <button
                    className="btn btn-quiet"
                    onClick={() => setPreviewIndex((value) => Math.max(0, value - 1))}
                    disabled={previewIndex === 0}
                  >
                    上一张
                  </button>
                  {previewIndex < previewCards.length - 1 ? (
                    <button
                      className="btn btn-primary"
                      onClick={() => setPreviewIndex((value) => value + 1)}
                    >
                      下一张
                    </button>
                  ) : reviewing ? (
                    <button
                      className="btn btn-primary"
                      onClick={() => {
                        setCardsOpen(false);
                        setOpenWord(null);
                        setPreviewIndex(0);
                        setPhase("summary");
                      }}
                    >
                      查看总结
                    </button>
                  ) : (
                    <button className="btn btn-primary" onClick={() => void startQuiz(detail.list_no)}>
                      开始做题
                    </button>
                  )}
                </div>
              </section>
            )}

            {phase === "settings" && (
              <div className="settings" aria-live="polite">
                <section className="settings-row" aria-labelledby="settings-grading">
                  <div className="settings-copy">
                    <h2 id="settings-grading">判分接口</h2>
                    <p>OpenAI 兼容接口用于全句翻译判分；未配置时自动转自评。</p>
                  </div>
                  <div className="settings-panel">
                    <div className="panel-head">
                      <span className="panel-title">连接</span>
                      <span className={settings?.api_key_set ? "pill pill-done" : "pill pill-idle"}>
                        {settings?.api_key_set ? "已保存密钥" : "未配置密钥"}
                      </span>
                    </div>
                    <label className="field">
                      <span>Base URL</span>
                      <input
                        value={draft.baseUrl}
                        onChange={(event) => setDraft({ ...draft, baseUrl: event.target.value })}
                        placeholder="https://api.openai.com/v1"
                        autoComplete="off"
                      />
                    </label>
                    <label className="field">
                      <span>模型名</span>
                      <input
                        value={draft.model}
                        onChange={(event) => setDraft({ ...draft, model: event.target.value })}
                        placeholder="gpt-4o-mini"
                        autoComplete="off"
                      />
                    </label>
                    <label className="field">
                      <span>API 密钥</span>
                      <input
                        value={draft.apiKey}
                        onChange={(event) => setDraft({ ...draft, apiKey: event.target.value })}
                        placeholder={settings?.api_key_set ? "已保存，留空则不修改" : "未配置"}
                        autoComplete="off"
                      />
                    </label>
                    <p className="panel-note">
                      密钥只保存在本机数据库，输入框明文显示以便粘贴，已保存的值不回填；导出文件也不包含密钥。
                    </p>
                    {settingsSaved && <p className="panel-ok">设置已保存。</p>}
                    <button className="btn btn-primary" onClick={saveSettings}>
                      保存设置
                    </button>
                  </div>
                </section>

                <section className="settings-row" aria-labelledby="settings-data">
                  <div className="settings-copy">
                    <h2 id="settings-data">数据与备份</h2>
                    <p>导出包含轮次、逐题结果、待清与首错快照和设置，不含 API 密钥。</p>
                  </div>
                  <div className="settings-panel">
                    <div className="panel-head">
                      <span className="panel-title">本地文件</span>
                      <span className="panel-note-inline">JSON</span>
                    </div>
                    <div className="settings-actions">
                      <button className="btn btn-quiet" onClick={exportBackup} disabled={backupBusy}>
                        导出备份
                      </button>
                      <button className="btn btn-quiet" onClick={importBackup} disabled={backupBusy}>
                        导入备份
                      </button>
                    </div>
                    {backupNote && (
                      <p className="panel-ok" role="status">
                        {backupNote}
                      </p>
                    )}
                  </div>
                </section>

                <section className="settings-row" aria-labelledby="settings-sync">
                  <div className="settings-copy">
                    <h2 id="settings-sync">WebDAV 同步</h2>
                    <p>
                      用坚果云 WebDAV 在设备之间搬运进度：上传用本机覆盖云端，下载用云端覆盖本机，不做合并。
                    </p>
                    <p>
                      用户名填坚果云账号邮箱；密码填「账户信息 → 安全选项 → 第三方应用管理」生成的第三方应用密码，不是登录密码。
                    </p>
                    <p>地址必须指向具体的进度文件，不能以 / 结尾；首次上传会自动创建 recitebook 文件夹。</p>
                    <p>同步文件是完整备份，不含判分接口的 API 密钥，也不含 WebDAV 密码。</p>
                  </div>
                  <div className="settings-panel">
                    <div className="panel-head">
                      <span className="panel-title">云端文件</span>
                      <span className={syncSettings?.password_set ? "pill pill-done" : "pill pill-idle"}>
                        {syncSettings?.password_set ? "已保存密码" : "未保存密码"}
                      </span>
                    </div>
                    <label className="field">
                      <span>地址</span>
                      <input
                        value={syncDraft.url}
                        onChange={(event) => setSyncDraft({ ...syncDraft, url: event.target.value })}
                        placeholder={SYNC_DEFAULT_URL}
                        autoComplete="off"
                        spellCheck={false}
                      />
                    </label>
                    {syncDraft.url.trim().toLowerCase().startsWith("http://") && (
                      <p className="notice notice-bad" role="alert">
                        当前地址使用 http://，WebDAV 用户名与密码会以未加密的明文传输；建议改用 https://
                        地址。
                      </p>
                    )}
                    <label className="field">
                      <span>用户名</span>
                      <input
                        value={syncDraft.username}
                        onChange={(event) =>
                          setSyncDraft({ ...syncDraft, username: event.target.value })
                        }
                        placeholder="坚果云账号邮箱"
                        autoComplete="off"
                      />
                    </label>
                    <label className="field">
                      <span>密码</span>
                      <input
                        value={syncDraft.password}
                        onChange={(event) =>
                          setSyncDraft({ ...syncDraft, password: event.target.value })
                        }
                        placeholder={
                          syncSettings?.password_set ? "已保存，留空则不修改" : "第三方应用密码"
                        }
                        autoComplete="off"
                      />
                    </label>
                    <p className="panel-note">
                      密码只存在本机数据库，输入框明文显示以便粘贴，已保存的值不回填；导出文件也不包含。留空保存不会改动已存密码。
                    </p>
                    {syncSaved && <p className="panel-ok">同步配置已保存。</p>}
                    <button
                      className="btn btn-primary"
                      onClick={saveSyncSettings}
                      disabled={syncBusy !== null}
                    >
                      保存同步配置
                    </button>
                    <div className="panel-head">
                      <span className="panel-title">进度同步</span>
                      <span className="panel-note-inline">
                        {lastSyncedAt ? `上次同步 ${formatSyncTime(lastSyncedAt)}` : "尚未同步"}
                      </span>
                    </div>
                    <div className="settings-actions">
                      <button
                        className="btn btn-quiet"
                        onClick={() => {
                          setSyncConfirm("upload");
                          setSyncNote(null);
                        }}
                        disabled={syncBusy !== null}
                      >
                        上传到云端
                      </button>
                      <button
                        className="btn btn-quiet"
                        onClick={() => {
                          setSyncConfirm("download");
                          setSyncNote(null);
                        }}
                        disabled={syncBusy !== null}
                      >
                        从云端下载
                      </button>
                    </div>
                    {syncConfirm && (
                      <>
                        <p className="panel-note" role="alert">
                          {syncConfirm === "upload"
                            ? "确认上传？将用本机覆盖云端，云端现有的进度会被替换。"
                            : "确认下载？将用云端覆盖本机，本机现有的进度会被替换。"}
                        </p>
                        <div className="settings-actions">
                          <button className="btn btn-primary" onClick={() => runSync(syncConfirm)}>
                            确认
                          </button>
                          <button className="btn btn-quiet" onClick={() => setSyncConfirm(null)}>
                            取消
                          </button>
                        </div>
                      </>
                    )}
                    {syncBusy && (
                      <p className="panel-note" role="status">
                        {syncBusy === "save"
                          ? "正在保存配置…"
                          : syncBusy === "upload"
                            ? "正在上传到云端…"
                            : "正在从云端下载…"}
                      </p>
                    )}
                    {syncNote && (
                      <p className="panel-ok" role="status">
                        {syncNote}
                      </p>
                    )}
                  </div>
                </section>
              </div>
            )}

            {phase === "queue" && (
              <>
                <section className="cover" aria-labelledby="cover-title" aria-live="polite">
                  {nextList ? (
                    <>
                      <span className="cover-no num" aria-hidden="true">
                        {String(nextList.list_no).padStart(2, "0")}
                      </span>
                      <div className="cover-main">
                        <h1 id="cover-title">{nextList.title}</h1>
                        <p className="cover-meta num">
                          队列第 {nextPosition} 位 · 待清 {nextPending} 题 · 首错 {nextFirstWrong} 题
                        </p>
                        <p className="cover-hint">进入后先略览本 List 的词卡，再开始作答。</p>
                      </div>
                      <div className="cover-act">
                        <span className="stamp-round num" aria-hidden="true">
                          第 {round} 轮
                        </span>
                        <button
                          className="btn btn-primary"
                          onClick={() => enter(nextList.list_no)}
                        >
                          开始背诵
                        </button>
                      </div>
                    </>
                  ) : (
                    <>
                      <span className="cover-no" aria-hidden="true">
                        —
                      </span>
                      <div className="cover-main">
                        <h1 id="cover-title">
                          {state ? "本轮 List 已清空" : "正在读取进度"}
                        </h1>
                        <p className="cover-meta">
                          {state ? "开启下一轮后只重考本轮的首错题。" : "进度读取中，稍候再开始。"}
                        </p>
                        {state && !canStartRound && (
                          <p className="cover-hint">每个 List 本轮至少作答一次后可开启。</p>
                        )}
                      </div>
                      <div className="cover-act">
                        <button
                          className="btn btn-primary"
                          disabled={!state || !canStartRound}
                          onClick={startRound}
                        >
                          开始新一轮
                        </button>
                      </div>
                    </>
                  )}
                </section>

                <section className="toc" aria-labelledby="toc-title">
                  <div className="toc-head">
                    <h2 id="toc-title">目录 · 队列顺序</h2>
                    <span className="toc-cols" aria-hidden="true">
                      <span>待清</span>
                      <span>首错</span>
                      <span>状态</span>
                    </span>
                  </div>
                  <ol className="toc-list">
                    {queueRows.map((row) => {
                      const status = statusOf(row.list_no);
                      const tone = toneOf[status] ?? "idle";
                      const pending = pendingOf(row.list_no);
                      const firstWrong =
                        state?.lists?.find((item) => item.list_no === row.list_no)?.first_wrong
                          ?.length ?? 0;
                      const isNext = row.list_no === nextNo;
                      return (
                        <li key={row.list_no}>
                          <button
                            className={`toc-row is-${tone}${isNext ? " is-next" : ""}`}
                            onClick={() => enter(row.list_no)}
                            aria-current={isNext ? "step" : undefined}
                            aria-label={`List ${row.list_no} ${row.title}，${status}，待清 ${pending} 题，首错 ${firstWrong} 题`}
                          >
                            <span className="toc-no num" aria-hidden="true">
                              {String(row.list_no).padStart(2, "0")}
                            </span>
                            <span className="toc-title" aria-hidden="true">
                              {row.title}
                            </span>
                            <span className="toc-pending num" aria-hidden="true">
                              {pending}
                            </span>
                            <span className="toc-first num" aria-hidden="true">
                              {firstWrong}
                            </span>
                            <span className={`toc-status stamp stamp-${tone}`} aria-hidden="true">
                              {status}
                            </span>
                          </button>
                        </li>
                      );
                    })}
                  </ol>
                  <div className="toc-foot">
                    <p className="toc-counts num">
                      已清 {cleared} 个 · 进行中 {activeLists} · 待清 {pendingLists} · 未开始{" "}
                      {idleLists} · 待清 {pendingTotal} 题 · 首错 {firstWrongTotal} 题
                    </p>
                    <button className="btn btn-quiet" disabled={!canStartRound} onClick={startRound}>
                      开始新一轮
                    </button>
                  </div>
                </section>
              </>
            )}

            {(phase === "question" || phase === "feedback") && question && (
              <section className="quiz" aria-labelledby="quiz-stem">
                <div className="quiz-folio">
                  <span className="quiz-folio-list num">
                    LIST {String(detail?.list_no ?? 0).padStart(2, "0")} · {detail?.title}
                  </span>
                  <span className="quiz-folio-meta num">
                    第 {step + 1} / {quizTotal} 题 · 待清 {quizPending}
                  </span>
                  <span className="quiz-folio-grade">
                    {gradingLabel}
                    {isTranslate ? " · 80 分算对" : ""}
                  </span>
                </div>

                <div
                  className="progress"
                  role="progressbar"
                  aria-valuemin={1}
                  aria-valuemax={Math.max(quizTotal, 1)}
                  aria-valuenow={step + 1}
                  aria-label={`第 ${step + 1} 题，共 ${quizTotal} 题`}
                >
                  <span
                    style={{
                      width: `${Math.min(100, ((step + 1) / Math.max(quizTotal, 1)) * 100)}%`,
                    }}
                  />
                </div>

                <p className="stem" id="quiz-stem">
                  {question.stem}
                </p>

                {isTranslate ? (
                  <textarea
                    className="answer-area"
                    rows={3}
                    value={text}
                    onChange={(event) => setText(event.target.value)}
                    placeholder={isTranslateWord ? "填写该词在句中的含义" : "填写整句译文"}
                    aria-label="译文"
                    disabled={phase === "feedback"}
                  />
                ) : isFill ? (
                  isMultiFill ? (
                    <div className="fields">
                      {fillAnswers.map((_, index) => (
                        <label className="field" key={index}>
                          <span>第 {index + 1} 空</span>
                          <input
                            value={blanks[index] ?? ""}
                            onChange={(event) => {
                              const next = [...blanks];
                              next[index] = event.target.value;
                              setBlanks(next);
                            }}
                            placeholder={`填写第 ${index + 1} 空`}
                            aria-label={`第 ${index + 1} 空答案`}
                            disabled={phase === "feedback"}
                          />
                        </label>
                      ))}
                      <p className="hint">按空分别填写，仅忽略大小写与首尾空格。</p>
                    </div>
                  ) : (
                    <div className="fields">
                      <label className="field">
                        <span>答案</span>
                        <input
                          value={text}
                          onChange={(event) => setText(event.target.value)}
                          placeholder="填写英文单词"
                          aria-label="答案"
                          disabled={phase === "feedback"}
                        />
                      </label>
                      <p className="hint">
                        仅忽略大小写与首尾空格；需要变形时填正确形式（如 assume → assumed）。
                      </p>
                    </div>
                  )
                ) : (
                  <div className="options">
                    {(question.options ?? []).map((option, index) => {
                      const chosen = picked === index;
                      const revealed = phase === "feedback" && index === question.answer;
                      const missed = phase === "feedback" && chosen && !revealed;
                      const tone = revealed
                        ? " is-correct"
                        : missed
                          ? " is-wrong"
                          : chosen
                            ? " is-chosen"
                            : "";
                      return (
                        <button
                          key={option}
                          className={`option${tone}`}
                          disabled={phase === "feedback"}
                          onClick={() => setPicked(index)}
                          aria-pressed={chosen}
                          aria-label={`${String.fromCharCode(65 + index)}. ${option}`}
                        >
                          <span className="option-key num">{String.fromCharCode(65 + index)}</span>
                          <span className="option-text">{option}</span>
                          <span className="option-mark" aria-hidden="true">
                            {revealed ? "✓" : missed ? "✕" : chosen && phase === "question" ? "•" : ""}
                          </span>
                        </button>
                      );
                    })}
                  </div>
                )}

                {phase === "question" && selfAssess ? (
                  <div aria-live="polite">
                    <p className={isTranslateWord ? "hint" : "hint hint-bad"}>
                      {isTranslateWord
                        ? "单词翻译按四档自评给分：0 / 50 / 80 / 100，80 分及以上算对。"
                        : "模型判分不可用（未配置密钥、断网或超时）。请自评："}
                    </p>
                    <div className="assess">
                      {SELF_LEVELS.map((level) => (
                        <button
                          key={level.label}
                          className="assess-btn"
                          onClick={() => assess(level)}
                        >
                          {level.label}
                        </button>
                      ))}
                    </div>
                  </div>
                ) : phase === "question" ? (
                  <div className="quiz-action">
                    <button
                      className="btn btn-primary"
                      disabled={!canSubmit || grading}
                      onClick={submit}
                    >
                      {grading ? "判分中…" : isTranslateWord ? "自评给分" : "提交"}
                    </button>
                  </div>
                ) : (
                  <div className="feedback" aria-live="polite">
                    <div className="feedback-head">
                      <span
                        className={`verdict ${feedback?.correct ? "verdict-good" : "verdict-bad"}`}
                      >
                        {feedback?.lookup ? "查词判错" : feedback?.correct ? "答对" : "答错"}
                      </span>
                      {typeof feedback?.score === "number" && (
                        <span className="feedback-score num">
                          {feedback.score}
                          <span className="feedback-unit"> / 100</span>
                        </span>
                      )}
                      <span className="feedback-via">
                        {feedback?.lookup
                          ? "查词"
                          : feedback?.self
                            ? "自评"
                            : isTranslateSentence
                              ? "LLM 判分"
                              : "自动判分"}
                      </span>
                    </div>
                    {feedback?.issues && feedback.issues.length > 0 && (
                      <ul className="feedback-issues">
                        {feedback.issues.map((issue) => (
                          <li key={issue}>{issue}</li>
                        ))}
                      </ul>
                    )}
                    {feedback?.comment && <p className="feedback-comment">{feedback.comment}</p>}
                    {feedback?.self && <p className="hint">未使用模型判分，本题按自评记录。</p>}
                    <div className="answer">
                      <span className="answer-label">{isTranslate ? "参考答案" : "正确答案"}</span>
                      <p className="answer-text">
                        {feedback?.answer ||
                          (isTranslateWord
                            ? "见词卡释义"
                            : isTranslate
                              ? "题库未提供，按原句判分"
                              : "")}
                      </p>
                    </div>
                    <p className="hint">
                      绑定词：
                      {(question.slots ?? []).map((slot) => slot.word).join("、") || "无"}
                    </p>
                    <div className="quiz-action feedback-actions">
                      <button className="btn btn-primary" onClick={advance}>
                        {state?.quiz ? "下一题" : "查看结果"}
                      </button>
                      <button
                        className="btn btn-quiet"
                        onClick={openChat}
                        aria-expanded={chatOpen}
                        aria-controls="chat-panel"
                      >
                        询问
                      </button>
                    </div>

                    {chatOpen && (
                      <div className="chat" id="chat-panel">
                        <div className="chat-head">
                          <h2 className="chat-title">询问</h2>
                          <span className="chat-sub">结合本题与绑定词卡</span>
                        </div>
                        <div className="chat-log">
                          {chatMessages.map((message, index) => (
                            <div
                              key={`${message.role}-${index}`}
                              className={`chat-msg ${
                                message.role === "user" ? "chat-msg-user" : "chat-msg-assistant"
                              }`}
                            >
                              <p className="chat-msg-text">{message.content}</p>
                            </div>
                          ))}
                          {chatMessages.length === 0 && !chatBusy && (
                            <p className="chat-empty">问一道题，从这里开始。</p>
                          )}
                          {chatBusy && (
                            <div className="chat-msg chat-msg-assistant">
                              <p className="chat-msg-text">
                                {chatStream}
                                <span className="chat-pending" aria-hidden="true">
                                  …
                                </span>
                              </p>
                            </div>
                          )}
                        </div>
                        {chatError && (
                          <p className="chat-error" role="status">
                            {chatError}
                          </p>
                        )}
                        <form
                          className="chat-compose"
                          onSubmit={(event) => {
                            event.preventDefault();
                            sendChat(chatDraft);
                          }}
                        >
                          <input
                            className="chat-input"
                            value={chatDraft}
                            onChange={(event) => setChatDraft(event.target.value)}
                            placeholder="继续追问…"
                            aria-label="追问内容"
                          />
                          <button className="btn btn-primary" type="submit" disabled={chatBusy}>
                            发送
                          </button>
                        </form>
                      </div>
                    )}
                  </div>
                )}

                <div className="quiz-foot">
                  <p>整题全对才算对</p>
                  <div className="quiz-foot-actions">
                    <button className="btn btn-quiet" onClick={() => setCardsOpen((value) => !value)}>
                      {cardsOpen ? "收起词卡" : "回看词卡"}
                    </button>
                    <button className="btn btn-quiet" onClick={jumpOut}>
                      跳出本 List
                    </button>
                  </div>
                </div>

                {cardsOpen && detail && (
                  <div className="cards" aria-label="本 List 词卡">
                    {detail.association?.scenes?.length ? (
                      <details className="association">
                        <summary className="association-outline">
                          {detail.association.outline || "类目联想"}
                          <span className="association-toggle" aria-hidden="true" />
                        </summary>
                        <div className="association-scenes">
                          {detail.association.scenes.map((scene) => (
                            <p key={scene.title} className="association-scene">
                              <span>{scene.title}</span>
                              {scene.story}
                            </p>
                          ))}
                        </div>
                      </details>
                    ) : null}
                    <p className="hint">
                      {phase === "question"
                        ? "回看词卡不扣分；点开具体单词查看释义，本题将整题判错。"
                        : "本题已作答，词卡仅供回看，不再改变状态。"}
                    </p>
                    <div className="word-list">
                      {(detail.words ?? []).map((word) => {
                        const expanded = openWord === word.spelling;
                        return (
                          <div key={word.spelling} className="word-row">
                            <button
                              className="word-toggle"
                              aria-expanded={expanded}
                              onClick={() => lookup(word.spelling)}
                            >
                              <span className="word-spelling">{word.spelling}</span>
                              {word.ipa && <span className="word-ipa">{word.ipa}</span>}
                            </button>
                            {expanded && (
                              <div className="word-body">
                                {(word.senses ?? []).map((sense) => (
                                  <p key={`${sense.pos}-${sense.meaning_cn}`}>
                                    <span className="word-pos">{sense.pos}</span> {sense.meaning_cn}
                                  </p>
                                ))}
                                {word.mnemonic && <p className="word-mnemonic">妙记：{word.mnemonic}</p>}
                                {(word.notes ?? []).map((note) => (
                                  <p key={`${note.marker}-${note.label_raw}`} className="word-note">
                                    {note.marker} {note.label_raw}：{note.body}
                                  </p>
                                ))}
                              </div>
                            )}
                          </div>
                        );
                      })}
                    </div>
                  </div>
                )}
              </section>
            )}

            {phase === "summary" && detail && (
              <section className="summary" aria-live="polite">
                <h2 className="summary-title">
                  {reviewing ? "本轮结果" : summaryPending > 0 ? "本组完成" : "本 List 完成"}
                </h2>

                <p className="summary-line">
                  {reviewing
                    ? "本轮已清。复习只读，不产生新作答。"
                    : summaryPending > 0
                      ? `还剩 ${summaryPending} 道待清，默认继续本 List。`
                      : nextListNo > 0
                        ? `错题已清空，已从队列移除。即将自动进入 List ${nextListNo}，返回队列可取消。`
                        : "错题已清空，本 List 已完成。"}
                </p>

                <dl className="summary-facts">
                  <div>
                    <dt>本轮对</dt>
                    <dd className="num">{wordCorrect}</dd>
                  </div>
                  <div>
                    <dt>本轮错</dt>
                    <dd className="num">{wordWrong}</dd>
                  </div>
                  <div>
                    <dt>未考</dt>
                    <dd className="num">{wordUntested}</dd>
                  </div>
                </dl>

                <ul className="word-results">
                  {words.map((word) => {
                    const tone =
                      word.result === Result.Correct
                        ? "done"
                        : word.result === Result.Wrong
                          ? "bad"
                          : "idle";
                    return (
                      <li key={word.spelling}>
                        <span className="word-result-spelling">{word.spelling}</span>
                        <span className={`word-result is-${tone}`}>
                          <span className={`mark mark-${tone}`} aria-hidden="true" />
                          {tone === "done" ? "本轮对" : tone === "bad" ? "本轮错" : "未考"}
                        </span>
                      </li>
                    );
                  })}
                </ul>

                <div className="summary-actions">
                  {summaryPending > 0 && (
                    <button className="btn btn-primary" onClick={() => enter(detail.list_no)}>
                      继续本 List
                    </button>
                  )}
                  <button
                    className={summaryPending > 0 ? "btn btn-quiet" : "btn btn-primary"}
                    onClick={() => {
                      setPhase("queue");
                      setDetail(null);
                      setActiveId(null);
                      setFeedback(null);
                      setPreviewIndex(0);
                      setReviewing(false);
                    }}
                  >
                    返回队列
                  </button>
                </div>
              </section>
            )}
          </main>

          <footer className="page-foot">
            <span>考研英语 · 循环背诵</span>
            <span>题库内嵌，离线可用。</span>
          </footer>
        </div>
      </div>
    </div>
  );
}
