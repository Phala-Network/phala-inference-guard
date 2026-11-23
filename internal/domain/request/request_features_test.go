package request

import "testing"

func TestParseTPSRequestShapeClassifiesRequestFeatures(t *testing.T) {
	tests := []struct {
		name     string
		endpoint EndpointKind
		body     string
		want     RequestFeatures
	}{
		{name: "chat defaults", endpoint: EndpointChatCompletions, body: `{"messages":[]}`},
		{name: "empty object", endpoint: EndpointChatCompletions, body: `{}`},
		{name: "chat logprobs without top", endpoint: EndpointChatCompletions, body: `{"logprobs":true}`, want: RequestFeatures{Logprobs: LogprobsZero}},
		{name: "chat top logprobs", endpoint: EndpointChatCompletions, body: `{"logprobs":true,"top_logprobs":5}`, want: RequestFeatures{Logprobs: LogprobsOneToFive}},
		{name: "chat top logprobs twenty", endpoint: EndpointChatCompletions, body: `{"top_logprobs":20,"logprobs":true}`, want: RequestFeatures{Logprobs: LogprobsSixToTwenty}},
		{name: "chat top logprobs over twenty", endpoint: EndpointChatCompletions, body: `{"logprobs":true,"top_logprobs":21}`, want: RequestFeatures{Logprobs: LogprobsOverTwenty}},
		{name: "chat logprobs disabled", endpoint: EndpointChatCompletions, body: `{"logprobs":false,"top_logprobs":3}`},
		{name: "chat logprobs null", endpoint: EndpointChatCompletions, body: `{"logprobs":null,"top_logprobs":null}`},
		{name: "chat logprobs invalid", endpoint: EndpointChatCompletions, body: `{"logprobs":"yes"}`, want: RequestFeatures{Logprobs: LogprobsInvalid}},
		{name: "chat top logprobs invalid", endpoint: EndpointChatCompletions, body: `{"logprobs":true,"top_logprobs":-1}`, want: RequestFeatures{Logprobs: LogprobsInvalid}},
		{name: "chat duplicate keeps last", endpoint: EndpointChatCompletions, body: `{"logprobs":true,"logprobs":false}`},
		{name: "completions logprobs zero", endpoint: EndpointCompletions, body: `{"prompt":"p","logprobs":0}`, want: RequestFeatures{Logprobs: LogprobsZero}},
		{name: "completions logprobs five", endpoint: EndpointCompletions, body: `{"prompt":"p","logprobs":5}`, want: RequestFeatures{Logprobs: LogprobsOneToFive}},
		{name: "completions logprobs bool", endpoint: EndpointCompletions, body: `{"prompt":"p","logprobs":true}`, want: RequestFeatures{Logprobs: LogprobsInvalid}},
		{name: "completions ignores top logprobs", endpoint: EndpointCompletions, body: `{"prompt":"p","top_logprobs":5}`},
		{name: "responses include logprobs", endpoint: EndpointResponses, body: `{"input":"x","include":["reasoning.encrypted_content","message.output_text.logprobs"]}`, want: RequestFeatures{Logprobs: LogprobsZero}},
		{name: "responses top logprobs", endpoint: EndpointResponses, body: `{"input":"x","top_logprobs":8}`, want: RequestFeatures{Logprobs: LogprobsSixToTwenty}},
		{name: "responses unrelated include", endpoint: EndpointResponses, body: `{"input":"x","include":["file_search_call.results"]}`},
		{name: "tool choice auto with tools", endpoint: EndpointChatCompletions, body: `{"tools":[{"type":"function"}],"tool_choice":"auto"}`, want: RequestFeatures{ToolChoice: ToolChoiceAuto, ToolsPresent: true}},
		{name: "tool choice none", endpoint: EndpointChatCompletions, body: `{"tool_choice":"none"}`, want: RequestFeatures{ToolChoice: ToolChoiceNone}},
		{name: "tool choice required", endpoint: EndpointChatCompletions, body: `{"tool_choice":"required"}`, want: RequestFeatures{ToolChoice: ToolChoiceRequired}},
		{name: "tool choice escaped", endpoint: EndpointChatCompletions, body: `{"tool_choice":"required"}`, want: RequestFeatures{ToolChoice: ToolChoiceRequired}},
		{name: "chat named tool", endpoint: EndpointChatCompletions, body: `{"tool_choice":{"type":"function","function":{"name":"secret_fn"}}}`, want: RequestFeatures{ToolChoice: ToolChoiceNamed}},
		{name: "responses named tool", endpoint: EndpointResponses, body: `{"tool_choice":{"name":"secret_fn","type":"function"}}`, want: RequestFeatures{ToolChoice: ToolChoiceNamed}},
		{name: "hosted tool choice", endpoint: EndpointResponses, body: `{"tool_choice":{"type":"web_search_preview"}}`, want: RequestFeatures{ToolChoice: ToolChoiceOther}},
		{name: "unknown tool choice", endpoint: EndpointChatCompletions, body: `{"tool_choice":"secret_fn"}`, want: RequestFeatures{ToolChoice: ToolChoiceOther}},
		{name: "numeric tool choice", endpoint: EndpointChatCompletions, body: `{"tool_choice":1}`, want: RequestFeatures{ToolChoice: ToolChoiceOther}},
		{name: "null tool choice and empty tools", endpoint: EndpointChatCompletions, body: `{"tool_choice":null,"tools":[ ]}`},
		{name: "text format", endpoint: EndpointChatCompletions, body: `{"response_format":{"type":"text"}}`, want: RequestFeatures{ResponseFormat: ResponseFormatText}},
		{name: "json object", endpoint: EndpointChatCompletions, body: `{"response_format":{"type":"json_object"}}`, want: RequestFeatures{ResponseFormat: ResponseFormatJSONObject}},
		{name: "json schema", endpoint: EndpointChatCompletions, body: `{"response_format":{"json_schema":{"name":"n","schema":{"type":"object"}},"type":"json_schema"}}`, want: RequestFeatures{ResponseFormat: ResponseFormatJSONSchema}},
		{name: "structural tag", endpoint: EndpointChatCompletions, body: `{"response_format":{"type":"structural_tag","structures":[]}}`, want: RequestFeatures{ResponseFormat: ResponseFormatStructuralTag}},
		{name: "unknown format type", endpoint: EndpointChatCompletions, body: `{"response_format":{"type":"secret"}}`, want: RequestFeatures{ResponseFormat: ResponseFormatOther}},
		{name: "format without type", endpoint: EndpointChatCompletions, body: `{"response_format":"json"}`, want: RequestFeatures{ResponseFormat: ResponseFormatOther}},
		{name: "completions response format", endpoint: EndpointCompletions, body: `{"prompt":"p","response_format":{"type":"json_object"}}`, want: RequestFeatures{ResponseFormat: ResponseFormatJSONObject}},
		{name: "sglang regex", endpoint: EndpointCompletions, body: `{"prompt":"p","regex":"[a-z]+"}`, want: RequestFeatures{ResponseFormat: ResponseFormatRegex}},
		{name: "sglang ebnf", endpoint: EndpointChatCompletions, body: `{"ebnf":"root ::= x"}`, want: RequestFeatures{ResponseFormat: ResponseFormatGrammar}},
		{name: "sglang json schema", endpoint: EndpointCompletions, body: `{"prompt":"p","json_schema":"{}"}`, want: RequestFeatures{ResponseFormat: ResponseFormatJSONSchema}},
		{name: "vllm guided choice", endpoint: EndpointChatCompletions, body: `{"guided_choice":["a","b"]}`, want: RequestFeatures{ResponseFormat: ResponseFormatChoice}},
		{name: "null extension", endpoint: EndpointChatCompletions, body: `{"regex":null}`},
		{name: "extension overrides text", endpoint: EndpointChatCompletions, body: `{"response_format":{"type":"text"},"regex":"x"}`, want: RequestFeatures{ResponseFormat: ResponseFormatRegex}},
		{name: "response format wins over extension", endpoint: EndpointChatCompletions, body: `{"regex":"x","response_format":{"type":"json_object"}}`, want: RequestFeatures{ResponseFormat: ResponseFormatJSONObject}},
		{name: "responses text format", endpoint: EndpointResponses, body: `{"input":"x","text":{"format":{"type":"json_schema","name":"n","schema":{}}}}`, want: RequestFeatures{ResponseFormat: ResponseFormatJSONSchema}},
		{name: "responses ignores response format", endpoint: EndpointResponses, body: `{"input":"x","response_format":{"type":"json_object"}}`},
		{name: "responses text without format", endpoint: EndpointResponses, body: `{"input":"x","text":{"verbosity":"low"}}`},
		{name: "nested fields are ignored", endpoint: EndpointChatCompletions, body: `{"messages":[{"logprobs":true,"tool_choice":"required","response_format":{"type":"json_object"}}]}`},
		{name: "unsupported shape keeps features", endpoint: EndpointChatCompletions, body: `{"n":0,"tool_choice":"required"}`, want: RequestFeatures{ToolChoice: ToolChoiceRequired}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			shape, valid := ParseTPSRequestShape([]byte(test.body), test.endpoint)
			if !valid {
				t.Fatalf("shape=%+v valid=false", shape)
			}
			if shape.Features != test.want {
				t.Fatalf("features=%+v want %+v", shape.Features, test.want)
			}
		})
	}
}

func TestParseTPSRequestShapeDropsFeaturesForMalformedJSON(t *testing.T) {
	for _, body := range []string{
		`{"tool_choice":"required"`,
		`{"tool_choice":"required",}`,
		`{"response_format":{"type":"json_object"}`,
		`["tool_choice","required"]`,
		``,
	} {
		shape, valid := ParseTPSRequestShape([]byte(body), EndpointChatCompletions)
		if valid || shape.Features != (RequestFeatures{}) {
			t.Errorf("malformed body=%q shape=%+v valid=%t", body, shape, valid)
		}
	}
}
