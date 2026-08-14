package acp

import "strings"

// executionEnvNote is prepended once per ACP session so models running inside
// agy do not try to call parent-harness tools inherited in the task prompt.
const executionEnvNote = "Execution environment note: You are running inside `agy` (Google Antigravity CLI), orchestrated from a parent agent session. Tool names, schemas, or tool instructions from that parent session are not available here. Use only the tools provided in this `agy` session."

func applyExecutionEnvNote(prompt string, alreadyInjected bool) string {
	if alreadyInjected || hasExecutionEnvNote(prompt) {
		return prompt
	}
	return executionEnvNote + "\n\n" + prompt
}

func hasExecutionEnvNote(prompt string) bool {
	trimmed := strings.TrimLeft(prompt, " \t\r\n")
	return strings.HasPrefix(trimmed, executionEnvNote)
}
