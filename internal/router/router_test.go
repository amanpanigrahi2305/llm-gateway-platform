package router

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/providers"
)

// fakeProvider records the model it was asked for.
type fakeProvider struct {
	name      string
	lastModel string
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Chat(_ context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	f.lastModel = req.Model
	return &providers.ChatResponse{Content: "from " + f.name}, nil
}

func (f *fakeProvider) ChatStream(_ context.Context, req providers.ChatRequest, emit func(providers.Chunk) error) error {
	f.lastModel = req.Model
	return emit(providers.Chunk{Content: "from " + f.name})
}

func newTestRouter(t *testing.T) (*Router, *fakeProvider, *fakeProvider) {
	t.Helper()
	primary, backup := &fakeProvider{name: "primary"}, &fakeProvider{name: "backup"}
	r, err := New(
		map[string][]string{
			"fast":  {"primary/model-a", "backup/model-b"},
			"local": {"backup/qwen2.5:0.5b"},
		},
		map[string]providers.Provider{"primary": primary, "backup": backup},
	)
	if err != nil {
		t.Fatal(err)
	}
	return r, primary, backup
}

func TestResolveKeepsOrder(t *testing.T) {
	r, _, _ := newTestRouter(t)
	targets, err := r.Resolve("fast")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].String() != "primary/model-a" || targets[1].String() != "backup/model-b" {
		t.Fatalf("targets = %v", targets)
	}
}

func TestResolveUnknownAlias(t *testing.T) {
	r, _, _ := newTestRouter(t)
	if _, err := r.Resolve("slow"); !errors.Is(err, ErrUnknownAlias) {
		t.Fatalf("err = %v, want ErrUnknownAlias", err)
	}
}

func TestChatUsesFirstTargetAndItsModel(t *testing.T) {
	r, primary, backup := newTestRouter(t)
	resp, target, err := r.Chat(context.Background(), "fast", providers.ChatRequest{Model: "fast"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "from primary" || target.String() != "primary/model-a" {
		t.Errorf("resp = %q from %s", resp.Content, target)
	}
	if primary.lastModel != "model-a" || backup.lastModel != "" {
		t.Errorf("primary got model %q, backup got %q", primary.lastModel, backup.lastModel)
	}
}

func TestChatStreamPassesTargetToEmit(t *testing.T) {
	r, _, backup := newTestRouter(t)
	var seen []string
	target, err := r.ChatStream(context.Background(), "local", providers.ChatRequest{}, func(tg Target, c providers.Chunk) error {
		seen = append(seen, tg.String()+": "+c.Content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if target.String() != "backup/qwen2.5:0.5b" || backup.lastModel != "qwen2.5:0.5b" {
		t.Errorf("target = %s, model = %q", target, backup.lastModel)
	}
	if !slices.Equal(seen, []string{"backup/qwen2.5:0.5b: from backup"}) {
		t.Errorf("emitted = %v", seen)
	}
}

func TestNewRejectsUnknownProvider(t *testing.T) {
	_, err := New(map[string][]string{"fast": {"ghost/model"}}, map[string]providers.Provider{})
	if err == nil {
		t.Fatal("New accepted a target with an unknown provider")
	}
}

func TestAliasesSorted(t *testing.T) {
	r, _, _ := newTestRouter(t)
	if got := r.Aliases(); !slices.Equal(got, []string{"fast", "local"}) {
		t.Fatalf("Aliases() = %v", got)
	}
}
