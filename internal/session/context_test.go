package session

import (
	"sync"
	"testing"
)

func TestContext_Lifecycle(t *testing.T) {
	ctx := NewContext("sess_test", "/workspace")

	if ctx.ID != "sess_test" {
		t.Fatalf("expected ID sess_test, got %q", ctx.ID)
	}
	if ctx.Cwd != "/workspace" {
		t.Fatalf("expected Cwd /workspace, got %q", ctx.Cwd)
	}
	if ctx.GetMode() != ModeNativeConversation {
		t.Fatal("expected initial mode to be NativeConversation")
	}
	if ctx.GetConversationID() != "" {
		t.Fatal("expected empty conversation ID initially")
	}
	if ctx.GetTurnCount() != 0 {
		t.Fatal("expected turn count 0 initially")
	}
}

func TestContext_AddMessages(t *testing.T) {
	ctx := NewContext("sess_test", "/workspace")

	ctx.AddUserMessage("hello")
	ctx.AddAssistantMessage("hi there")
	ctx.AddUserMessage("how are you")

	transcript := ctx.GetTranscript()
	if len(transcript) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(transcript))
	}

	if transcript[0].Role != RoleUser || transcript[0].Content != "hello" {
		t.Fatalf("unexpected first message: %+v", transcript[0])
	}
	if transcript[1].Role != RoleAssistant || transcript[1].Content != "hi there" {
		t.Fatalf("unexpected second message: %+v", transcript[1])
	}
	if transcript[2].Role != RoleUser || transcript[2].Content != "how are you" {
		t.Fatalf("unexpected third message: %+v", transcript[2])
	}

	if ctx.GetTurnCount() != 2 {
		t.Fatalf("expected turn count 2, got %d", ctx.GetTurnCount())
	}
}

func TestContext_ConversationID(t *testing.T) {
	ctx := NewContext("sess_test", "/workspace")

	ctx.SetConversationID("abc-123-def")
	if ctx.GetConversationID() != "abc-123-def" {
		t.Fatalf("expected conversation ID abc-123-def, got %q", ctx.GetConversationID())
	}
}

func TestContext_FallbackMode(t *testing.T) {
	ctx := NewContext("sess_test", "/workspace")

	if ctx.GetMode() != ModeNativeConversation {
		t.Fatal("expected initial mode NativeConversation")
	}

	ctx.SwitchToFallback()
	if ctx.GetMode() != ModeFallbackContext {
		t.Fatal("expected mode to switch to FallbackContext")
	}
}

func TestContext_TranscriptIsCopy(t *testing.T) {
	ctx := NewContext("sess_test", "/workspace")
	ctx.AddUserMessage("hello")

	transcript := ctx.GetTranscript()
	transcript[0].Content = "modified"

	original := ctx.GetTranscript()
	if original[0].Content != "hello" {
		t.Fatal("GetTranscript should return a copy, not a reference")
	}
}

func TestContext_ConcurrentAccess(t *testing.T) {
	ctx := NewContext("sess_test", "/workspace")
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			ctx.AddUserMessage("msg")
		}()
		go func() {
			defer wg.Done()
			_ = ctx.GetTranscript()
		}()
	}
	wg.Wait()
}

func TestContext_Close(t *testing.T) {
	ctx := NewContext("sess_test", "/workspace")
	if ctx.IsClosed() {
		t.Fatal("expected not closed initially")
	}
	ctx.Close()
	if !ctx.IsClosed() {
		t.Fatal("expected closed after Close()")
	}
}
