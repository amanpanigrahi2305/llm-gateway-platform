# Phase 1 smoke test - raw output

- Date (UTC): 2026-11-22T02:52Z
- Machine: AMD Ryzen AI 9 HX PRO 375 w/ Radeon 890M, 24 CPUs, WSL2 kernel 6.6.87.2-microsoft-standard-WSL2
- Go go1.27.4; Ollama 0.40.17 in Docker with qwen2.5:0.5b (CPU)
- Gateway commit: 64f67a7
- `$GW_KEY` is a local dev key from `gateway gen-key`; its value is not shown.

## 1. List models

```
$ curl -s localhost:8080/v1/models -H "Authorization: Bearer $GW_KEY"
HTTP/1.1 200 OK
Content-Type: application/json

{
  "data": [
    {
      "id": "fast",
      "object": "model",
      "created": 0,
      "owned_by": "llm-gateway"
    },
    {
      "id": "local",
      "object": "model",
      "created": 0,
      "owned_by": "llm-gateway"
    }
  ],
  "object": "list"
}
```

## 2. Same curl, mock (alias fast)

```
$ curl -s localhost:8080/v1/chat/completions -H "Authorization: Bearer $GW_KEY" -H "Content-Type: application/json" -d '{"model":"fast","messages":[{"role":"user","content":"Say hello in five words."}],"max_tokens":30}'
HTTP/1.1 200 OK
Content-Type: application/json
X-Gateway-Model: mock-1
X-Gateway-Provider: mock
X-Request-Id: chatcmpl-dd96dbbc36addcc7167a87ab

{
  "id": "chatcmpl-dd96dbbc36addcc7167a87ab",
  "object": "chat.completion",
  "created": 1795315963,
  "model": "fast",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "The quick brown fox jumps over the lazy dog while the mock model streams tokens at a steady pace The"
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 6,
    "completion_tokens": 20,
    "total_tokens": 26
  }
}
```

## 3. Same curl, Ollama (alias local)

```
$ curl -s localhost:8080/v1/chat/completions -H "Authorization: Bearer $GW_KEY" -H "Content-Type: application/json" -d '{"model":"local","messages":[{"role":"user","content":"Say hello in five words."}],"max_tokens":30}'
HTTP/1.1 200 OK
Content-Type: application/json
X-Gateway-Model: qwen2.5:0.5b
X-Gateway-Provider: ollama
X-Request-Id: chatcmpl-2cc50b8a6b91cae251642b3c

{
  "id": "chatcmpl-2cc50b8a6b91cae251642b3c",
  "object": "chat.completion",
  "created": 1795315976,
  "model": "local",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "Hello, how are you?"
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 35,
    "completion_tokens": 7,
    "total_tokens": 42
  }
}
```

## 4. Streaming, mock (alias fast)

```
$ curl -N localhost:8080/v1/chat/completions -H "Authorization: Bearer $GW_KEY" -H "Content-Type: application/json" -d '{"model":"fast","messages":[{"role":"user","content":"Count from 1 to 5."}],"max_tokens":8,"stream":true,"stream_options":{"include_usage":true}}'
HTTP/1.1 200 OK
Content-Type: text/event-stream
X-Gateway-Model: mock-1
X-Gateway-Provider: mock

data: {"id":"chatcmpl-2bbb6eb0447de36695824eb0","object":"chat.completion.chunk","created":1795315977,"model":"fast","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"chatcmpl-2bbb6eb0447de36695824eb0","object":"chat.completion.chunk","created":1795315977,"model":"fast","choices":[{"index":0,"delta":{"content":"The"},"finish_reason":null}]}

data: {"id":"chatcmpl-2bbb6eb0447de36695824eb0","object":"chat.completion.chunk","created":1795315977,"model":"fast","choices":[{"index":0,"delta":{"content":" quick"},"finish_reason":null}]}

data: {"id":"chatcmpl-2bbb6eb0447de36695824eb0","object":"chat.completion.chunk","created":1795315977,"model":"fast","choices":[{"index":0,"delta":{"content":" brown"},"finish_reason":null}]}

data: {"id":"chatcmpl-2bbb6eb0447de36695824eb0","object":"chat.completion.chunk","created":1795315977,"model":"fast","choices":[{"index":0,"delta":{"content":" fox"},"finish_reason":null}]}

data: {"id":"chatcmpl-2bbb6eb0447de36695824eb0","object":"chat.completion.chunk","created":1795315977,"model":"fast","choices":[{"index":0,"delta":{"content":" jumps"},"finish_reason":null}]}

data: {"id":"chatcmpl-2bbb6eb0447de36695824eb0","object":"chat.completion.chunk","created":1795315977,"model":"fast","choices":[{"index":0,"delta":{"content":" over"},"finish_reason":null}]}

data: {"id":"chatcmpl-2bbb6eb0447de36695824eb0","object":"chat.completion.chunk","created":1795315977,"model":"fast","choices":[{"index":0,"delta":{"content":" the"},"finish_reason":null}]}

data: {"id":"chatcmpl-2bbb6eb0447de36695824eb0","object":"chat.completion.chunk","created":1795315977,"model":"fast","choices":[{"index":0,"delta":{"content":" lazy"},"finish_reason":null}]}

data: {"id":"chatcmpl-2bbb6eb0447de36695824eb0","object":"chat.completion.chunk","created":1795315977,"model":"fast","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}

data: {"id":"chatcmpl-2bbb6eb0447de36695824eb0","object":"chat.completion.chunk","created":1795315977,"model":"fast","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":8,"total_tokens":13}}

data: [DONE]

```

## 5. Streaming, Ollama (alias local)

```
$ curl -N localhost:8080/v1/chat/completions -H "Authorization: Bearer $GW_KEY" -H "Content-Type: application/json" -d '{"model":"local","messages":[{"role":"user","content":"Count from 1 to 5."}],"max_tokens":30,"stream":true,"stream_options":{"include_usage":true}}'
HTTP/1.1 200 OK
Content-Type: text/event-stream
X-Gateway-Model: qwen2.5:0.5b
X-Gateway-Provider: ollama

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":"Sure"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":","},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":" here"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":" are"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":" the"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":" numbers"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":" from"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":" "},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":"1"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":" to"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":" "},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":"5"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":":\n\n"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":"1"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":"\n"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":"2"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":"\n"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":"3"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":"\n"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":"4"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":"\n"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{"content":"5"},"finish_reason":null}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"chatcmpl-ab503de2809d6fe82dbdf20c","object":"chat.completion.chunk","created":1795315977,"model":"local","choices":[],"usage":{"prompt_tokens":37,"completion_tokens":23,"total_tokens":60}}

data: [DONE]

```

## 6. No API key -> 401

```
$ curl -s localhost:8080/v1/chat/completions -d '{"model":"fast","messages":[{"role":"user","content":"Say hello in five words."}],"max_tokens":30}'
HTTP/1.1 401 Unauthorized
Content-Type: application/json
X-Request-Id: chatcmpl-cc86ffe4a147a413ad0a53ad

{
  "error": {
    "code": "missing_api_key",
    "message": "missing API key: send Authorization: Bearer <key>",
    "type": "invalid_request_error"
  }
}
```

## 7. Unknown model -> 404

```
$ curl -s localhost:8080/v1/chat/completions -H "Authorization: Bearer $GW_KEY" -H "Content-Type: application/json" -d '{"model":"gpt-9",...}'
HTTP/1.1 404 Not Found
Content-Type: application/json
X-Request-Id: chatcmpl-e853ae0ea4b659f5e21eb460

{
  "error": {
    "code": "model_not_found",
    "message": "model \"gpt-9\" does not exist; GET /v1/models lists the models you can use",
    "type": "invalid_request_error"
  }
}
```

## 8. Mock outage switch on -> gateway returns 502

```
$ curl -s -X PATCH localhost:9090/admin/settings -d '{"outage":true}'
{"latency_ms":0,"ttft_ms":50,"tokens_per_sec":100,"response_tokens":20,"error_rate":0,"error_status":500,"outage":true,"omit_usage":false}
```

## 8b. Request during outage

```
$ curl -s localhost:8080/v1/chat/completions -H "Authorization: Bearer $GW_KEY" -H "Content-Type: application/json" -d '{"model":"fast","messages":[{"role":"user","content":"Say hello in five words."}],"max_tokens":30}'
HTTP/1.1 502 Bad Gateway
Content-Type: application/json
X-Gateway-Model: mock-1
X-Gateway-Provider: mock
X-Request-Id: chatcmpl-b8d7cb323ea07761dfa62e84

{
  "error": {
    "code": "upstream_error",
    "message": "upstream provider mock failed (status 503): mock outage switch is on",
    "type": "api_error"
  }
}
```

## 9. Mock hides usage -> gateway estimates tokens

```
$ curl -s -X PATCH localhost:9090/admin/settings -d '{"omit_usage":true}'
{"latency_ms":0,"ttft_ms":50,"tokens_per_sec":100,"response_tokens":20,"error_rate":0,"error_status":500,"outage":false,"omit_usage":true}
```

## 9b. Streaming without upstream usage

```
$ curl -N localhost:8080/v1/chat/completions -H "Authorization: Bearer $GW_KEY" -H "Content-Type: application/json" -d '{"model":"fast","messages":[{"role":"user","content":"Count from 1 to 5."}],"max_tokens":8,"stream":true,"stream_options":{"include_usage":true}}'
HTTP/1.1 200 OK
Content-Type: text/event-stream
X-Gateway-Model: mock-1
X-Gateway-Provider: mock

data: {"id":"chatcmpl-8a5988064120c70ad7682b10","object":"chat.completion.chunk","created":1795315978,"model":"fast","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"chatcmpl-8a5988064120c70ad7682b10","object":"chat.completion.chunk","created":1795315978,"model":"fast","choices":[{"index":0,"delta":{"content":"The"},"finish_reason":null}]}

data: {"id":"chatcmpl-8a5988064120c70ad7682b10","object":"chat.completion.chunk","created":1795315978,"model":"fast","choices":[{"index":0,"delta":{"content":" quick"},"finish_reason":null}]}

data: {"id":"chatcmpl-8a5988064120c70ad7682b10","object":"chat.completion.chunk","created":1795315978,"model":"fast","choices":[{"index":0,"delta":{"content":" brown"},"finish_reason":null}]}

data: {"id":"chatcmpl-8a5988064120c70ad7682b10","object":"chat.completion.chunk","created":1795315978,"model":"fast","choices":[{"index":0,"delta":{"content":" fox"},"finish_reason":null}]}

data: {"id":"chatcmpl-8a5988064120c70ad7682b10","object":"chat.completion.chunk","created":1795315978,"model":"fast","choices":[{"index":0,"delta":{"content":" jumps"},"finish_reason":null}]}

data: {"id":"chatcmpl-8a5988064120c70ad7682b10","object":"chat.completion.chunk","created":1795315978,"model":"fast","choices":[{"index":0,"delta":{"content":" over"},"finish_reason":null}]}

data: {"id":"chatcmpl-8a5988064120c70ad7682b10","object":"chat.completion.chunk","created":1795315978,"model":"fast","choices":[{"index":0,"delta":{"content":" the"},"finish_reason":null}]}

data: {"id":"chatcmpl-8a5988064120c70ad7682b10","object":"chat.completion.chunk","created":1795315978,"model":"fast","choices":[{"index":0,"delta":{"content":" lazy"},"finish_reason":null}]}

data: {"id":"chatcmpl-8a5988064120c70ad7682b10","object":"chat.completion.chunk","created":1795315978,"model":"fast","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}

data: {"id":"chatcmpl-8a5988064120c70ad7682b10","object":"chat.completion.chunk","created":1795315978,"model":"fast","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}

data: [DONE]

```

## 10. Official OpenAI Python SDK (only base_url changed)

```
$ python sdk_test.py   # openai 3.57.0
models: ['fast', 'local']
[fast] non-stream: 'The quick brown fox jumps over the lazy dog while the mock model streams tokens at a steady pace The' | finish=stop | usage=6/20
[fast] stream:     'The quick brown fox jumps over the lazy dog while the mock model streams tokens at a steady pace The' | usage=5/20
[local] non-stream: 'Hello, how are you?' | finish=stop | usage=35/7
[local] stream:     '1\n2\n3\n4\n5' | usage=37/10
```

## 11. Graceful shutdown while a stream is running (drain_delay 2s, shutdown_timeout 30s)

```
mock set to 5 tokens/s and 15 tokens, so the stream needs about 3 s
SIGTERM sent 0.5 s after the stream started
GET /readyz during drain: HTTP 503
stream finished 3.0 s after start; content chunks: 15; ended with [DONE]: yes
gateway exited 2.6 s after SIGTERM with exit code 0

gateway log, shutdown lines:
{"time":"2026-11-21T20:53:21.961923784-06:00","level":"INFO","msg":"shutdown started","drain_delay":"2s","shutdown_timeout":"30s"}
{"time":"2026-11-21T20:53:24.528989942-06:00","level":"INFO","msg":"shutdown complete"}
```

## 12. Usage records from the gateway log (metadata only)

```
{"alias":"fast","provider":"mock","model":"mock-1","stream":false,"status":200,"error_type":"","latency_ms":272,"ttft_ms":0,"prompt_tokens":6,"completion_tokens":20,"estimated":false,"cost_usd":0.000046}
{"alias":"local","provider":"ollama","model":"qwen2.5:0.5b","stream":false,"status":200,"error_type":"","latency_ms":16403,"ttft_ms":0,"prompt_tokens":35,"completion_tokens":7,"estimated":false,"cost_usd":0}
{"alias":"fast","provider":"mock","model":"mock-1","stream":true,"status":200,"error_type":"","latency_ms":127,"ttft_ms":53,"prompt_tokens":5,"completion_tokens":8,"estimated":false,"cost_usd":0.000021}
{"alias":"local","provider":"ollama","model":"qwen2.5:0.5b","stream":true,"status":200,"error_type":"","latency_ms":1031,"ttft_ms":260,"prompt_tokens":37,"completion_tokens":23,"estimated":false,"cost_usd":0}
{"alias":"","provider":"","model":"","stream":false,"status":401,"error_type":"auth","latency_ms":0,"ttft_ms":0,"prompt_tokens":0,"completion_tokens":0,"estimated":false,"cost_usd":0}
{"alias":"gpt-9","provider":"","model":"","stream":false,"status":404,"error_type":"unknown_model","latency_ms":0,"ttft_ms":0,"prompt_tokens":0,"completion_tokens":0,"estimated":false,"cost_usd":0}
{"alias":"fast","provider":"mock","model":"mock-1","stream":false,"status":502,"error_type":"upstream_error","latency_ms":1,"ttft_ms":0,"prompt_tokens":0,"completion_tokens":0,"estimated":false,"cost_usd":0}
{"alias":"fast","provider":"mock","model":"mock-1","stream":true,"status":200,"error_type":"","latency_ms":131,"ttft_ms":56,"prompt_tokens":5,"completion_tokens":10,"estimated":true,"cost_usd":0.000025}
{"alias":"fast","provider":"mock","model":"mock-1","stream":false,"status":200,"error_type":"","latency_ms":270,"ttft_ms":0,"prompt_tokens":6,"completion_tokens":20,"estimated":false,"cost_usd":0.000046}
{"alias":"fast","provider":"mock","model":"mock-1","stream":true,"status":200,"error_type":"","latency_ms":256,"ttft_ms":54,"prompt_tokens":5,"completion_tokens":20,"estimated":false,"cost_usd":0.000045}
{"alias":"local","provider":"ollama","model":"qwen2.5:0.5b","stream":false,"status":200,"error_type":"","latency_ms":352,"ttft_ms":0,"prompt_tokens":35,"completion_tokens":7,"estimated":false,"cost_usd":0}
{"alias":"local","provider":"ollama","model":"qwen2.5:0.5b","stream":true,"status":200,"error_type":"","latency_ms":281,"ttft_ms":96,"prompt_tokens":37,"completion_tokens":10,"estimated":false,"cost_usd":0}
{"alias":"fast","provider":"mock","model":"mock-1","stream":true,"status":200,"error_type":"","latency_ms":2993,"ttft_ms":1,"prompt_tokens":3,"completion_tokens":15,"estimated":false,"cost_usd":0.000033}
```
