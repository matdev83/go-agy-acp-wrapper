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
1. Dumps the full in-memory transcript to a structured markdown temp file
2. Passes the file to agy as a fresh one-shot prompt
3. Parses the response and continues tracking state in memory

### Long Prompt Handling
Prompts exceeding the configurable byte threshold (default 8KB) are written to
temp files and referenced via `@filepath` syntax to avoid CLI argument limits.

### Response Extraction
When `agy --print` produces no stdout (a known platform-specific issue), the
wrapper extracts the model's response directly from agy's transcript log at
`~/.gemini/antigravity-cli/brain/<UUID>/.system_generated/logs/transcript.jsonl`.

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

## Configuration

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `AGY_BINARY` | `agy.exe` (Windows) / `agy` (Linux) | Path to the agy binary |
| `AGY_MODEL` | _(empty = agy default)_ | Default model for new sessions (e.g. `gemini-2.5-flash`, `Gemini 3.1 Pro (High)`) |
| `AGY_PROMPT_THRESHOLD` | `8000` | Byte threshold above which prompts are written to temp files |
| `AGY_TIMEOUT_SECONDS` | `300` | Per-turn execution timeout in seconds |

### Model Selection

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
    "value": "gemini-2.5-flash"
  }
}
```

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
- The wrapper uses `--dangerously-skip-permissions` to avoid interactive permission
  prompts. This bypasses agy's safety checks.

## Platform Notes

- **Windows**: Uses `agy.exe`. Process termination is immediate (no SIGTERM).
- **Linux**: Uses `agy`. Sends SIGTERM with 5s grace period before SIGKILL on cancel.
- All file paths use `filepath.Join` and `os.UserHomeDir()` for portability.
- Temp files are created in `os.TempDir()/go-agy-acp` with 0600 permissions.
