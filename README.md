# go-agy-acp-wrapper

A cross-platform Go service that wraps Google's Antigravity CLI (`agy`) behind an
[Agent Client Protocol (ACP)](https://agentclientprotocol.com) v1 interface.
This enables IDE integrations, orchestrators, and automation tools to communicate
with `agy` using the standardized ACP JSON-RPC protocol over stdio.

## Architecture

```
┌──────────────┐    JSON-RPC/stdio    ┌──────────────────────┐
│  ACP Client  │◄────────────────────►│  go-agy-acp-wrapper  │
│  (IDE/Editor)│                      │                      │
└──────────────┘                      │  ┌────────────────┐  │
                                      │  │ Session Context │  │
                                      │  │   Manager      │  │
                                      │  └───────┬────────┘  │
                                      │          │           │
                                      │  ┌───────▼────────┐  │
                                      │  │   Agy Runner   │  │
                                      │  └───────┬────────┘  │
                                      └──────────┼───────────┘
                                                 │
                                      ┌──────────▼───────────┐
                                      │  agy --print / --conv │
                                      │  (subprocess)         │
                                      └───────────────────────┘
```

## Multi-Turn Conversation Strategy

The wrapper uses a **hybrid** approach to maintain multi-turn sessions:

### Primary: Native Conversation Resumption
1. First turn: `agy --print "prompt"` creates a new agy conversation
2. The wrapper discovers the conversation UUID from agy's local state
3. Subsequent turns: `agy --conversation <UUID> --print "next prompt"` resumes natively

### Fallback: Virtual Context Window
If native conversation resumption fails, the wrapper:
1. Dumps the full in-memory transcript to a structured markdown file under the session workdir
2. Passes the file to agy as a fresh one-shot prompt
3. Parses the response and continues tracking state in memory

### Long Prompt Handling
Prompts exceeding the configurable byte threshold (default 8KB) are written under
`.go-agy-acp-wrapper/<session-id>/` in the ACP session workdir and referenced via
`@filepath` syntax to avoid CLI argument limits. Stale files are removed when the
first session for a workdir starts, when the last session in that workdir closes,
and on wrapper shutdown.

### Provider 429 / quota continue

A mid-turn Gemini `429` / `RESOURCE_EXHAUSTED` used to fail the ACP `session/prompt`
immediately (or, before fail-fast, hang until `--timeout-seconds`). The wrapper now
keeps that prompt open and retries the **native** `agy --conversation` turn:

1. Parse `retryDelay` / "retry in …" / `Retry-After` when present; otherwise use
   exponential backoff (`2s` … `60s`).
2. Wait up to the remaining per-turn timeout (or `--quota-retry-max-wait-seconds`).
3. Stream a short "waiting … before retry" notice so the ACP client is not silent.
4. If the failed attempt already produced a conversation ID, send a continue cue
   instead of replaying the user prompt.
5. After the attempt budget is exhausted, still return ACP `-32003` and **do not**
   dump fallback context (that would burn more quota).

This is independent of the virtual-context fallback used for other native-resume
failures.

### Response Extraction
When `agy --print` produces no stdout (a known platform-specific issue), the
wrapper extracts the model's response directly from agy's transcript log at
`~/.gemini/antigravity-cli/brain/<UUID>/.system_generated/logs/transcript.jsonl`.

## Installation

Prebuilt archives for Windows, Linux, and macOS (`amd64` and `arm64`) are
available from [GitHub Releases](https://github.com/matdev83/go-agy-acp-wrapper/releases).
Each release includes a `checksums.txt` file with SHA-256 checksums.

Download the archive for your platform, extract `go-agy-acp-wrapper` (or
`go-agy-acp-wrapper.exe` on Windows), and place it on your `PATH`.

## Prerequisites

- **agy** installed and authenticated
  - Windows: `curl -fsSL https://antigravity.google/cli/install.cmd -o install.cmd && install.cmd`
  - Linux/macOS: `curl -fsSL https://antigravity.google/cli/install.sh | bash`
- **Go 1.21+** (for building from source)
- agy must be authenticated (`agy` interactive login on first use)

## Building

```bash
# Native build
go build -o bin/go-agy-acp-wrapper ./cmd/go-agy-acp-wrapper
go build -o bin/acp-smoke ./cmd/acp-smoke

# Cross-compile for Linux from Windows
set GOOS=linux
set GOARCH=amd64
go build -o bin/go-agy-acp-wrapper-linux ./cmd/go-agy-acp-wrapper
```

## Running

The wrapper communicates over stdin/stdout using ACP's JSON-RPC protocol:

```bash
./bin/go-agy-acp-wrapper
```

An ACP client connects by spawning this binary and piping JSON-RPC messages.
The process keeps stdout reserved for ACP JSON-RPC traffic; operational logs go to stderr.

For launchers such as `llm-interactive-proxy`, runtime options can be provided as
flags instead of environment variables:

```bash
go-agy-acp-wrapper \
  --agy-binary agy \
  --model gemini-2.5-flash \
  --timeout-seconds 14400 \
  --prompt-threshold 8000
```

Use `go-agy-acp-wrapper --version` for executable validation without starting ACP.

## Configuration

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `AGY_BINARY` | `agy.exe` (Windows) / `agy` (Linux) | Path to the agy binary |
| `AGY_MODEL` | _(empty = catalog default)_ | Default model for new sessions (e.g. `gemini-3.6-flash-high`) |
| `AGY_PROMPT_THRESHOLD` | `8000` | Byte threshold above which prompts are written to temp files |
| `AGY_TIMEOUT_SECONDS` | `14400` (4 hours) | Per-turn execution timeout in seconds. Forwarded to agy as `--print-timeout` (agy's own print-mode default is 5 minutes). |
| `AGY_QUOTA_RETRY_ATTEMPTS` | `5` | How many `agy --print` invocations to make for one ACP prompt after a 429 / quota error (1 initial + retries). |
| `AGY_QUOTA_RETRY_MAX_WAIT_SECONDS` | `0` (remaining turn timeout) | Cap for a single backoff wait. `0` waits up to the remaining per-turn timeout (so a 3h quota reset can still resume inside a 4h turn). |
| `AGY_SKIP_PERMISSIONS` | `true` | Whether to pass `--dangerously-skip-permissions` to `agy`; set to `false` to opt out |
| `AGY_ACP_SKIP_ENV_NOTE` | `false` | Set to `true` to skip the once-per-session execution-environment steering note |

Equivalent CLI flags are available and override environment values:

| Flag | Description |
|------|-------------|
| `--agy-binary <path>` | Path to the agy binary |
| `--model <model>` | Default model for new sessions |
| `--prompt-threshold <bytes>` | Byte threshold above which prompts are written to workdir files |
| `--timeout-seconds <seconds>` | Per-turn execution timeout and agy `--print-timeout` |
| `--quota-retry-attempts <n>` | Native continue retries after 429 / quota errors |
| `--quota-retry-max-wait-seconds <seconds>` | Cap for a single 429 backoff (`0` = remaining turn timeout) |
| `--skip-permissions` | Force-enable `--dangerously-skip-permissions` |
| `--no-skip-permissions` | Opt out of `--dangerously-skip-permissions` |
| `--execution-env-note` | Force-enable the once-per-session execution-environment note |
| `--no-execution-env-note` | Opt out of the execution-environment note |
| `--version` | Print wrapper version and exit |

### Subagent execution-environment note

On the first `session/prompt` of each ACP session, the wrapper prepends a short
steering note so models running inside `agy` do not try to call parent-harness
tools that were copied into the task prompt. Later turns are left unchanged
because native `agy --conversation` resume already keeps the first-turn note.
The note is skipped when the incoming prompt already starts with it, and can be
disabled with `AGY_ACP_SKIP_ENV_NOTE=true` or `--no-execution-env-note`.

### Model Selection

Models are discovered automatically from `agy models` when the wrapper starts
(during ACP `initialize`, cached for the process lifetime). Native effort variants
are normalized into canonical provider/model identities. For example,
`gemini-3.6-flash-{low,medium,high}` is advertised as `google/gemini-3.6-flash`.
The separate ACP `reasoning_effort` session option selects the native agy variant.

The model can be configured at multiple levels:

1. **Environment variable**: Set `AGY_MODEL` to apply a default to all new sessions
2. **ACP `session/set_config_option`**: Clients can change the model per-session at runtime

The wrapper advertises available models via `configOptions` in the `session/new` response,
with category `"model"`. Clients can switch models by calling `session/set_config_option`:

```json
{
  "method": "session/set_config_option",
  "params": {
    "configId": "model",
    "sessionId": "sess_abc123",
    "value": "google/gemini-3.6-flash"
  }
}
```

Unknown model values and unsupported reasoning efforts return an error from
`session/set_config_option`. If `AGY_MODEL` is set to an unknown value, the wrapper
logs a warning and falls back to the catalog default (preferring
`google/gemini-3.8-flash`, then `google/gemini-3.7-flash`, then `google/gemini-3.6-flash`).

## Supported ACP Methods

| Method | Status |
|--------|--------|
| `initialize` | Supported |
| `authenticate` | Supported (no-op, agy handles its own auth) |
| `session/new` | Supported |
| `session/prompt` | Supported (multi-turn with conversation resumption) |
| `session/cancel` | Supported (kills agy process) |
| `session/close` | Supported (cleanup temp files + session state) |
| `session/update` | Supported (streams agent message chunks) |
| `session/list` | Not supported |
| `session/load` | Not supported |
| `session/resume` | Not supported |

## Running the Smoke Test

The smoke test spawns the wrapper and runs a 3-turn conversation:

```bash
# Build both binaries first
go build -o bin/go-agy-acp-wrapper ./cmd/go-agy-acp-wrapper
go build -o bin/acp-smoke ./cmd/acp-smoke

# Run (set WRAPPER_BIN to point to the wrapper binary)
WRAPPER_BIN=./bin/go-agy-acp-wrapper ./bin/acp-smoke
```

On Windows:
```powershell
$env:WRAPPER_BIN = ".\bin\go-agy-acp-wrapper.exe"
.\bin\acp-smoke.exe
```

## Running Tests

```bash
go test ./... -v
```

## Repository File Policy

Every file permitted in a commit is listed explicitly in `.release-files`. Add a
new entry there in the same commit as any legitimate new source, test, workflow,
or documentation file. Files not in the manifest are rejected regardless of
their name, extension, or directory.

Install the versioned pre-commit and pre-push hooks:

```bash
bash scripts/setup-hooks.sh
```

The pre-commit hook checks the complete staged tree, and the pre-push hook checks
every commit being sent. CI repeats the commit-by-commit check on every branch
push and pull request. To prevent bypasses from entering `main`, configure the
GitHub branch ruleset to require pull requests and the `Repo hygiene` status
check, and disable direct-push bypasses.

## Project Structure

```
cmd/
  go-agy-acp-wrapper/   ACP agent server binary
  acp-smoke/            E2E smoke test client
internal/
  acp/                  ACP Agent interface implementation
  agy/                  agy runner, conversation discovery, prompt file writer
  session/              Per-session context manager and concurrent store
  config/               Runtime configuration from env vars
```

## Known Limitations

- `agy --print` may not produce stdout in certain non-TTY environments on Windows.
  The wrapper mitigates this by reading agy's transcript.jsonl file as a fallback.
- Concurrent sessions in the same working directory may race on conversation ID
  discovery. Each ACP session should use a distinct cwd.
- agy authentication is handled externally; the wrapper cannot initiate auth flows.
- The wrapper uses `--dangerously-skip-permissions` by default to avoid interactive
  permission prompts. This bypasses agy's safety checks; opt out with
  `AGY_SKIP_PERMISSIONS=false` or `--no-skip-permissions`.
- Clients that spawn the wrapper with `--timeout-seconds` (including LIP
  `process_timeout`) override the 4-hour default. Keep that value high enough for
  long tool waits; agy's own `--print-timeout` default is 5 minutes.
- The execution-environment note is a mitigation, not a guarantee. Parent
  harnesses should still send a task rather than dumping their own tool catalog.

## Platform Notes

- **Windows**: Uses `agy.exe`. Process termination is immediate (no SIGTERM).
- **Linux**: Uses `agy`. Sends SIGTERM with 5s grace period before SIGKILL on cancel.
- All file paths use `filepath.Join` and `os.UserHomeDir()` for portability.
- Prompt/context files are created under `.go-agy-acp-wrapper/` in the ACP session
  workdir with 0600 permissions and are automatically cleaned.
