package acp

import "testing"

func TestApplyExecutionEnvNote_PrependsOnce(t *testing.T) {
	got := applyExecutionEnvNote("run the tests", false)
	want := executionEnvNote + "\n\nrun the tests"
	if got != want {
		t.Fatalf("unexpected prompt:\n%s", got)
	}
}

func TestApplyExecutionEnvNote_SkipsWhenAlreadyInjected(t *testing.T) {
	got := applyExecutionEnvNote("follow up", true)
	if got != "follow up" {
		t.Fatalf("expected original prompt, got %q", got)
	}
}

func TestApplyExecutionEnvNote_SkipsWhenPromptAlreadyStartsWithNote(t *testing.T) {
	prompt := "  \n" + executionEnvNote + "\n\nrun the tests"
	got := applyExecutionEnvNote(prompt, false)
	if got != prompt {
		t.Fatal("expected existing note to be left unchanged")
	}
}
