package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/providers"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/router"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/usage"
)

const maxBodyBytes = 8 << 20 // 8 MiB is plenty for text prompts

// chatRequest is the OpenAI request body. Unknown fields are ignored.
type chatRequest struct {
	Model         string        `json:"model"`
	Messages      []chatMessage `json:"messages"`
	Stream        bool          `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	Temperature         *float64 `json:"temperature"`
	TopP                *float64 `json:"top_p"`
	MaxTokens           *int     `json:"max_tokens"`
	MaxCompletionTokens *int     `json:"max_completion_tokens"`
	Stop                stopList `json:"stop"`
}

func (r chatRequest) includeUsage() bool {
	return r.StreamOptions != nil && r.StreamOptions.IncludeUsage
}

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// text accepts content as a string or as an array of text parts; OpenAI SDKs
// send plain text in both forms.
func (m chatMessage) text() (string, error) {
	if len(m.Content) == 0 {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return s, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m.Content, &parts); err != nil {
		return "", errors.New("content must be a string or an array of text parts")
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type != "text" {
			return "", fmt.Errorf("content part type %q is not supported", p.Type)
		}
		b.WriteString(p.Text)
	}
	return b.String(), nil
}

// stopList accepts "stop" as a string or as an array of strings.
type stopList []string

func (s *stopList) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		if one != "" {
			*s = stopList{one}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("stop must be a string or an array of strings")
	}
	*s = many
	return nil
}

// parseChat reads and checks the request body.
func parseChat(w http.ResponseWriter, r *http.Request) (chatRequest, providers.ChatRequest, *apiError) {
	fail := func(e *apiError) (chatRequest, providers.ChatRequest, *apiError) {
		return chatRequest{}, providers.ChatRequest{}, e
	}
	var req chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return fail(&apiError{http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large",
				"request body is larger than 8 MiB"})
		}
		return fail(badRequest("invalid JSON body: " + err.Error()))
	}
	if req.Model == "" {
		return fail(badRequest("model is required"))
	}
	if len(req.Messages) == 0 {
		return fail(badRequest("messages must not be empty"))
	}
	msgs := make([]providers.Message, 0, len(req.Messages))
	for i, m := range req.Messages {
		if m.Role == "" {
			return fail(badRequest(fmt.Sprintf("messages[%d].role is required", i)))
		}
		text, err := m.text()
		if err != nil {
			return fail(badRequest(fmt.Sprintf("messages[%d]: %v", i, err)))
		}
		msgs = append(msgs, providers.Message{Role: m.Role, Content: text})
	}
	maxTokens := req.MaxCompletionTokens
	if maxTokens == nil {
		maxTokens = req.MaxTokens
	}
	if maxTokens != nil && *maxTokens < 1 {
		return fail(badRequest("max_tokens must be at least 1"))
	}
	return req, providers.ChatRequest{
		Messages:    msgs,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   maxTokens,
		Stop:        req.Stop,
	}, nil
}

// chatCompletions handles POST /v1/chat/completions. Every request ends with
// exactly one usage record in the log: metadata only, never prompt or answer text.
func (s *Server) chatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := usage.Record{RequestID: newRequestID()}
	w.Header().Set("X-Request-Id", rec.RequestID)
	defer func() {
		rec.LatencyMS = time.Since(start).Milliseconds()
		s.logRecord(rec)
	}()

	key, ok := s.authenticate(w, r)
	if !ok {
		rec.Status, rec.ErrorType = http.StatusUnauthorized, "auth"
		return
	}
	rec.KeyID, rec.Owner, rec.Team = key.ID, key.Owner, key.Team

	req, preq, apiErr := parseChat(w, r)
	if apiErr != nil {
		rec.Status, rec.ErrorType = apiErr.Status, "bad_request"
		writeError(w, *apiErr)
		return
	}
	rec.Alias, rec.Stream = req.Model, req.Stream

	if _, err := s.router.Resolve(req.Model); err != nil {
		rec.Status, rec.ErrorType = http.StatusNotFound, "unknown_model"
		writeError(w, apiError{http.StatusNotFound, "invalid_request_error", "model_not_found",
			fmt.Sprintf("model %q does not exist; GET /v1/models lists the models you can use", req.Model)})
		return
	}
	if !key.Allows(req.Model) {
		rec.Status, rec.ErrorType = http.StatusForbidden, "model_not_allowed"
		writeError(w, apiError{http.StatusForbidden, "permission_error", "model_not_allowed",
			fmt.Sprintf("this API key may not use model %q", req.Model)})
		return
	}

	promptChars := 0
	for _, m := range preq.Messages {
		promptChars += utf8.RuneCountInString(m.Content)
	}
	if req.Stream {
		s.stream(w, r, req, preq, promptChars, start, &rec)
	} else {
		s.complete(w, r, req, preq, promptChars, &rec)
	}
}

// complete handles a normal (non-streaming) request.
func (s *Server) complete(w http.ResponseWriter, r *http.Request, req chatRequest, preq providers.ChatRequest,
	promptChars int, rec *usage.Record) {
	resp, out, err := s.router.Chat(r.Context(), req.Model, preq)
	s.setOutcome(w, rec, out)
	if err != nil {
		s.fail(w, r, err, rec)
		return
	}
	u := s.account(rec, out.Target, resp.Usage, promptChars, utf8.RuneCountInString(resp.Content))
	finish := resp.FinishReason
	if finish == "" {
		finish = providers.FinishStop
	}
	rec.Status = http.StatusOK
	writeJSON(w, http.StatusOK, completionBody{
		ID: rec.RequestID, Object: "chat.completion", Created: time.Now().Unix(), Model: req.Model,
		Choices: []completionChoice{{Message: message{Role: "assistant", Content: resp.Content}, FinishReason: finish}},
		Usage:   toUsageBody(u),
	})
}

// stream relays the answer as Server-Sent Events in OpenAI's chunk format.
// Headers are sent only when the first chunk arrives, so a failure before that
// still gets a normal JSON error with the right status code.
func (s *Server) stream(w http.ResponseWriter, r *http.Request, req chatRequest, preq providers.ChatRequest,
	promptChars int, start time.Time, rec *usage.Record) {
	rc := http.NewResponseController(w)
	created := time.Now().Unix()
	var (
		started, writeFailed, finishSent bool
		completionChars                  int
		reported                         *providers.Usage
	)

	sendData := func(v any) error {
		b, err := json.Marshal(v)
		if err == nil {
			_, err = fmt.Fprintf(w, "data: %s\n\n", b)
		}
		if err == nil {
			err = rc.Flush()
		}
		if err != nil {
			writeFailed = true
		}
		return err
	}
	chunk := func(d delta, finish string) chunkBody {
		c := chunkBody{ID: rec.RequestID, Object: "chat.completion.chunk", Created: created, Model: req.Model,
			Choices: []chunkChoice{{Delta: d}}}
		if finish != "" {
			c.Choices[0].FinishReason = &finish
			finishSent = true
		}
		return c
	}
	begin := func(t router.Target) error {
		started = true
		rec.TTFTMS = time.Since(start).Milliseconds()
		s.setTarget(w, rec, t)
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no") // ask proxies such as nginx not to buffer the stream
		w.WriteHeader(http.StatusOK)
		return sendData(chunk(delta{Role: "assistant"}, ""))
	}

	out, err := s.router.ChatStream(r.Context(), req.Model, preq, func(t router.Target, c providers.Chunk) error {
		if !started {
			if err := begin(t); err != nil {
				return err
			}
		}
		if c.Usage != nil {
			reported = c.Usage
		}
		if c.Content == "" && c.FinishReason == "" {
			return nil // usage only: the usage chunk is sent at the end
		}
		completionChars += utf8.RuneCountInString(c.Content)
		return sendData(chunk(delta{Content: c.Content}, c.FinishReason))
	})

	target := out.Target
	s.setOutcome(w, rec, out)
	switch {
	case err != nil && !started:
		s.fail(w, r, err, rec)
		return
	case err != nil:
		// The 200 status is already sent and cannot change. Like OpenAI, report
		// the error inside the stream and end it without [DONE].
		s.account(rec, target, reported, promptChars, completionChars)
		if writeFailed || clientGone(r, err) {
			rec.Status, rec.ErrorType = statusClientClosed, "client_closed"
			return
		}
		apiErr, errType := upstreamError(err)
		rec.Status, rec.ErrorType = apiErr.Status, errType+"_midstream"
		_ = sendData(map[string]any{"error": apiErr.body()})
		return
	}

	if !started && begin(target) != nil {
		rec.Status, rec.ErrorType = statusClientClosed, "client_closed"
		return
	}
	if !finishSent {
		_ = sendData(chunk(delta{}, providers.FinishStop))
	}
	u := s.account(rec, target, reported, promptChars, completionChars)
	if req.includeUsage() {
		_ = sendData(chunkBody{ID: rec.RequestID, Object: "chat.completion.chunk", Created: created, Model: req.Model,
			Choices: []chunkChoice{}, Usage: toUsageBody(u)})
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	_ = rc.Flush()
	rec.Status = http.StatusOK
	if writeFailed {
		rec.Status, rec.ErrorType = statusClientClosed, "client_closed"
	}
}

// setOutcome records which target served the request and how many tries it took.
// After a stream has started, the header part has no effect (headers are sent).
func (s *Server) setOutcome(w http.ResponseWriter, rec *usage.Record, out router.Outcome) {
	rec.Attempts, rec.Failovers = out.Attempts, out.Failovers
	s.setTarget(w, rec, out.Target)
}

// setTarget records which provider served the request and shows it in headers.
func (s *Server) setTarget(w http.ResponseWriter, rec *usage.Record, t router.Target) {
	if t.Provider == nil {
		return
	}
	rec.Provider, rec.Model = t.Provider.Name(), t.Model
	w.Header().Set("X-Gateway-Provider", rec.Provider)
	w.Header().Set("X-Gateway-Model", rec.Model)
}

// fail writes the error for a failed upstream call, unless the client is gone.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error, rec *usage.Record) {
	if clientGone(r, err) {
		rec.Status, rec.ErrorType = statusClientClosed, "client_closed"
		return
	}
	apiErr, errType := upstreamError(err)
	rec.Status, rec.ErrorType = apiErr.Status, errType
	writeError(w, apiErr)
}

func clientGone(r *http.Request, err error) bool {
	return r.Context().Err() != nil && errors.Is(err, context.Canceled)
}

// account fills in tokens and cost. When the provider reported no usage, it
// estimates tokens from text length and marks the record as estimated.
func (s *Server) account(rec *usage.Record, t router.Target, reported *providers.Usage,
	promptChars, completionChars int) providers.Usage {
	var u providers.Usage
	if reported != nil {
		u = *reported
	} else {
		u = providers.Usage{
			PromptTokens:     usage.EstimateTokens(promptChars),
			CompletionTokens: usage.EstimateTokens(completionChars),
		}
		rec.Estimated = true
	}
	rec.PromptTokens, rec.CompletionTokens = u.PromptTokens, u.CompletionTokens
	if t.Provider != nil {
		rec.CostUSD, _ = s.prices.Cost(t.String(), u.PromptTokens, u.CompletionTokens)
	}
	return u
}

// --- response shapes (OpenAI Chat Completions) ---

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type completionChoice struct {
	Index        int     `json:"index"`
	Message      message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type usageBody struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func toUsageBody(u providers.Usage) *usageBody {
	return &usageBody{u.PromptTokens, u.CompletionTokens, u.PromptTokens + u.CompletionTokens}
}

type completionBody struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   *usageBody         `json:"usage"`
}

type delta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type chunkChoice struct {
	Index        int     `json:"index"`
	Delta        delta   `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type chunkBody struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *usageBody    `json:"usage,omitempty"`
}
