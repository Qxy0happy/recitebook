# 循环背诵

Wails v3 (Go + React/TypeScript) app for cycling through the 29 word lists.
Bank JSON is embedded in the binary; progress and settings are stored in a local
SQLite database through the pure-Go `modernc.org/sqlite` driver (no CGO
dependency for the store).

## Round flow

Entering a list for the first time in a round opens its word cards one screen at
a time (类目联想导语 → scenes → words → 开始做题) before the first question.
The preview writes `list_states.previewed` the moment it opens, so skipping it,
or returning to the queue instead of starting, still counts for that round; a
new round clears the flag. Preview cards show every meaning, 妙记 and 考点 in
full and never count as a lookup: 查词 is only the in-quiz word toggle, which
marks the current question wrong. The engine method behind it is
`EngineService.Preview`, which starts no quiz.

## 题目询问

The feedback panel after a question carries a 询问 button, shown only once the
question is in `phase === "feedback"` — asking mid-question would leak the
answer. The button expands a conversation panel inside the feedback block rather
than opening a new screen, and the panel starts collapsed.

The first click on a question with no saved turns sends a fixed opening prompt
(`请讲解这道题的考点，并指出我回答里的问题。`) by itself; with saved history the
click only expands the panel. The reply streams into the panel as it arrives.
Every request rebuilds its context from the bank — list, question kind, prompt,
options, correct answer, your answer, the outcome and the bound word cards — so
bank edits apply immediately and no question text is stored.

The chat reuses the settings screen's Base URL, 模型名 and API 密钥, the same
OpenAI-compatible endpoint used for translation grading; there is no separate
chat setting. A question accepts further follow-up turns in the same panel, and
each finished exchange is saved to the local SQLite `chats` table keyed by
question id, so reopening the app and returning to that question's feedback
restores the conversation.

A failed request writes nothing to the database and leaves the typed text in the
composer for a retry. With no configured key, no network or a timeout, the panel
shows the readable error (未配置时为 `chat: 未配置可用的模型与密钥`) and the rest
of the quiz keeps working; no local answer is substituted.

## Prerequisites

- Go 1.26+
- Node 20+ with pnpm; the frontend is driven by Vite+ (`vp`)
- Tailwind CSS 4 (`tailwindcss` + `@tailwindcss/vite`, both 4.3.x); styles are
  utilities in `frontend/src/App.tsx`, `frontend/src/styles.css` only imports
  Tailwind and sets the theme tokens
- Wails v3 CLI: `go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.34`
- go-task (`task`)
- NSIS (`makensis`) for the Windows installer: `choco install nsis` or
  `scoop install nsis`

Android builds additionally need:

- JDK 17+ (verified with JBR 25)
- Android SDK with platform 35, build-tools, platform-tools, emulator and at
  least one NDK (27 or 28); set `ANDROID_HOME` to the SDK root
- `ANDROID_NDK_HOME` is optional: the newest NDK under `$ANDROID_HOME/ndk` is
  used automatically

The Gradle wrapper downloads Gradle 9.2.1 on first use (AGP 8.7.3, compileSdk
35). On Windows the NDK host tag is auto-detected as `windows-x86_64` and the
wrapper is invoked through `gradlew.bat`.

## Desktop

```sh
task build        # production build
task run          # build and run
```

## Windows installer

```sh
task package                     # NSIS installer -> bin/recitebook-amd64-installer.exe
task package INSTALL_SCOPE=user  # per-user install, no UAC prompt
```

`task package` defaults to NSIS on Windows. The first run downloads the WebView2
bootstrapper into `build/windows/nsis/`, so the installer also works on machines
without the runtime. The installer is unsigned: Windows SmartScreen warns until
a code-signing certificate is configured with `wails3 setup signing`.

CI builds the portable exe and the installer on every run; tag releases also
upload `recitebook-<tag>-setup.exe`.

## Formatting and linting

Go formatting and linting run from the module's tool dependencies, so no extra
installs are needed:

```sh
task fmt          # gofumpt over the Go tree + oxfmt over the frontend
task lint         # revive (wails/revive.toml) + oxlint
```

Frontend-only commands, run from `frontend/`:

```sh
vp run fmt        # format with oxfmt
vp run lint       # lint with oxlint (config in .oxlintrc.json)
vp run check      # oxfmt --check, oxlint and the TypeScript check together
```

`revive` only covers the application packages (`./internal/... .`); the
Wails-generated scaffolding under `build/` is excluded in `revive.toml`.
`oxfmt` and `oxlint` skip `bindings/` and `dist/`: `.prettierignore` is the
ignore filename oxfmt honours (an `.oxfmtignore` file is not read), and
`.oxlintrc.json` carries the matching `ignorePatterns`.

`@shadcn/lint` is registered as an Oxlint JS plugin through `jsPlugins` in
`.oxlintrc.json`. No shadcn rules are enabled yet: add `shadcn/*` rules there
once the design-system policies are decided. Rule docs:
<https://github.com/shadcn-ui/lint/blob/main/docs/rules.md>; configuration:
<https://github.com/shadcn-ui/lint/blob/main/docs/design-systems.md>.


## Verification

The Go tests in the root package embed `frontend/dist`, so build the frontend
once before running them:

```sh
cd frontend
vp install
vp run build
vp exec tsc --noEmit
cd ..
go test ./internal/... .
```

`TestEmbeddedBankIsConsistent` checks all 29 embedded lists, and `e2e_test.go`
drives the real `EngineService` plus a temporary SQLite database through the
five core flows (first pass, inner loop, jump-out, lookup, new round) and the
export/import round trip. No device or network is required.

Server-mode smoke test on Windows (the database lives beside the executable,
so run a copy from an isolated directory):

```bat
go build -tags server,production -o bin\recitebook-server.exe .
mkdir %TEMP%\recitebook-smoke
copy bin\recitebook-server.exe %TEMP%\recitebook-smoke\
cd %TEMP%\recitebook-smoke
set WAILS_SERVER_PORT=9347
set WAILS_SERVER_HOST=127.0.0.1
recitebook-server.exe
```

Open `http://127.0.0.1:9347/`. Android install/layout/back-button behaviour is
not covered by these checks; it needs a physical device or emulator.

## Android

```sh
task android:build ARCH=amd64    # cross-compile the Go shared library
task android:assemble:apk        # debug APK -> bin/recitebook.apk
task android:compile:go:shared ARCH=arm64   # add the second ABI, then assemble again
task android:package:fat         # release APK with arm64 + amd64
task android:run                 # build, install and launch (needs a device/emulator)
```

`ARCH` defaults to the host architecture; use `amd64` for an x86_64 emulator and
`arm64` for physical devices. The Go shared library embeds `frontend/dist`, so
the frontend is built first by the task graph, and `task android:package` passes
the production flags through to both halves. `task android:package` builds
arm64 only; `package:fat` builds both ABIs, which is what to use after a debug
build has left an `x86_64` library in `jniLibs`. Release APKs are signed with
the debug keystore unless `ANDROID_KEYSTORE_FILE` and the related variables are
set. `build/android/overlay.json` and all Gradle output are generated per
machine and are git-ignored.

`app/build.gradle` disables `lintVital` for release builds because AGP 8.7.3's
lint aborts on JBR's four-part version string (`25.0.4.1`). Remove that block
when the Gradle JVM reports a three-part version or AGP is upgraded.

The Java sources live in `com.wails.app` and must stay there: wails v3 hardcodes
the JNI entry points as `Java_com_wails_app_WailsBridge_*`, so another package
makes `nativeInit` fail with `UnsatisfiedLinkError` and the app exits right after
the launcher animation. The Gradle `namespace` follows the Java package;
`applicationId` stays `com.riyu.recitebook`, so the installed identity is
unchanged.

Gradle needs a JDK that can build the `android-35` system image; GraalVM 25.1.3
fails the `JdkImageTransform` `jlink` step. Build with JBR 25.0.4.1
(`JAVA_HOME=D:/scoop/apps/intellij-jbr25-sdk-jcef/25.0.4.1-b635.78`).

## Data location

- Windows: `data/recitebook.db` beside the executable.
- Android: the app-private files directory, i.e.
  `/data/data/<applicationId>/files/recitebook.db`.

## WebDAV sync

Settings → WebDAV 同步 moves progress between devices through a single JSON
file on a WebDAV server. Jianguoyun is the default target:

- URL (prefilled): `https://dav.jianguoyun.com/dav/recitebook/progress.json`
- Username: the Jianguoyun account email
- Password: a third-party app password from 账户信息 → 安全选项 → 第三方应用管理,
  not the account password

A Jianguoyun account root cannot hold files, only folders, so the address needs
a folder segment; the first upload creates `recitebook/` with `MKCOL`. The
address must point at a file and must not end in `/`.

The two buttons are independent overwrite operations. There is no merge and no
conflict detection:

- 上传到云端 replaces the remote file with this device's progress.
- 从云端下载 replaces this device's progress with the remote file.

Both ask for confirmation first. The remote file is the same v1 JSON backup
that 导出备份 writes, so it can also be restored with 导入备份. The SQLite
database never leaves the device, and the backup excludes both the LLM API key
and the WebDAV credentials. 题目询问 conversations stay in the local SQLite
`chats` table: neither the JSON backup nor the WebDAV sync file contains chat
history, and importing a backup leaves it in place. Sync runs only when a button
is pressed; there is no background, startup or shutdown sync.

Plain `http://` addresses are accepted, but the settings screen warns that the
Basic-auth credentials then travel unencrypted.

