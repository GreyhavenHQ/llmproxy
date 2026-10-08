package server

import "testing"

func TestOpenAIFirstContent(t *testing.T) {
	cases := map[string]bool{
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`:      false,
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":null}}]}`:    false,
		`data: {"choices":[{"index":0,"delta":{"content":"hi"}}]}`:                       true,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"hmm"}}]}`:            true,
		`data: {"choices":[{"index":0,"delta":{"reasoning":"hmm"}}]}`:                    true,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1"}]}}]}`: true,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[]}}]}`:                      false,
		`data: {"choices":[{"index":0,"text":"legacy"}]}`:                                true,
		`data: {"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`:         false,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`:              false,
		`data: [DONE]`:   false,
		`: keep-alive`:   false,
		`data: not json`: false,
		`data:{"choices":[{"index":0,"delta":{"content":"no space after the colon"}}]}`:        true,
		`data: {"choices":[{"index":0,"delta":{"content":[{"type":"text","text":"parts"}]}}]}`: true,
	}
	for line, want := range cases {
		if got := openAIFirstContent([]byte(line)); got != want {
			t.Errorf("openAIFirstContent(%s) = %v, want %v", line, got, want)
		}
	}
}

func TestAnthropicFirstContent(t *testing.T) {
	cases := map[string]bool{
		`event: content_block_delta`: false,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":1}}}`: false,
		`data: {"type":"ping"}`: false,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`: false,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a"}}`:  true,
		`data: {"type":"message_delta","usage":{"output_tokens":3}}`:                               false,
	}
	for line, want := range cases {
		if got := anthropicFirstContent([]byte(line)); got != want {
			t.Errorf("anthropicFirstContent(%s) = %v, want %v", line, got, want)
		}
	}
}
