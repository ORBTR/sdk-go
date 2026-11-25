package orbtr

// Ptr returns a pointer to v, for the optional fields of a params struct.
func Ptr[T any](v T) *T { return &v }

// String returns a pointer to s.
func String(s string) *string { return &s }

// Int returns a pointer to n.
func Int(n int64) *int64 { return &n }

// Float returns a pointer to f.
func Float(f float64) *float64 { return &f }

// Bool returns a pointer to b.
func Bool(b bool) *bool { return &b }

// UserMessage is a user message with text content.
func UserMessage(text string) MessageParam {
	return MessageParam{Role: "user", Content: StringOf[ContentBlockParam](text)}
}

// AssistantMessage is an assistant message with text content, for a conversation's earlier replies.
func AssistantMessage(text string) MessageParam {
	return MessageParam{Role: "assistant", Content: StringOf[ContentBlockParam](text)}
}

// MessageOfBlocks is a message whose content is blocks, such as images or tool results.
func MessageOfBlocks(role string, blocks ...ContentBlockParam) MessageParam {
	return MessageParam{Role: role, Content: ArrayOf(blocks...)}
}

// SystemPrompt is a system prompt of text, for MessageCreateParams.System.
func SystemPrompt(text string) *StringOr[TextBlockParam] {
	s := StringOf[TextBlockParam](text)
	return &s
}

// ChatMessage is an OpenAI-compatible chat message with text content.
func ChatMessage(role, text string) ChatMessageParam {
	return ChatMessageParam{Role: role, Content: StringOf[ChatContentPart](text)}
}
