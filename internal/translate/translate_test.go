package translate

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"undined-go/internal/config"
)

func TestFormatEmailForReading_UnwrapsProseAndQuotes(t *testing.T) {
	raw := `Hi Kernel Developer,

On Mon, Nov 10, 2026 at 10:00 AM Kernel Developer
<dev@example.com> wrote:
> Currently the ring buffer resize path holds the mutex across
> the entire page allocation loop, which can stall readers when
> memory is fragmented.
>
> >> Can we drop the lock before allocating the new pages and
> >> re-acquire it only when splicing the list?
>
> Yes, that makes sense as long as we serialize concurrent
> resize callers.

I tested this approach on a 64-core system under heavy trace
workload and confirmed the latency spike disappears:

  # trace-cmd record -e sched_wakeup
  latency_max_us: 18

Key changes in v2:
- Allocate pages outside buffer_mutex to avoid blocking readers
  during memory reclaim.
- Add a dedicated resize_lock mutex.

Signed-off-by: Kernel Developer <dev@example.com>
Reviewed-by: Reviewer One <reviewer@example.com>
---
 kernel/trace/ring_buffer.c | 4 ++--
 1 file changed, 2 insertions(+), 2 deletions(-)

diff --git a/kernel/trace/ring_buffer.c b/kernel/trace/ring_buffer.c
index 1111111..2222222 100644
--- a/kernel/trace/ring_buffer.c
+++ b/kernel/trace/ring_buffer.c
@@ -10,4 +10,4 @@
-	old_call();
+	new_call();`

	formatted := FormatEmailForReading(raw)

	// 1. Attribution header unwrapped into a single line
	wantAttr := "On Mon, Nov 10, 2026 at 10:00 AM Kernel Developer <dev@example.com> wrote:"
	if !strings.Contains(formatted, wantAttr) {
		t.Errorf("FormatEmailForReading() = \n%s\nwant substring %q", formatted, wantAttr)
	}

	// 2. Depth-1 quote unwrapped with "> " prefix
	wantQuote1 := "> Currently the ring buffer resize path holds the mutex across the entire page allocation loop, which can stall readers when memory is fragmented."
	if !strings.Contains(formatted, wantQuote1) {
		t.Errorf("FormatEmailForReading() = \n%s\nwant substring %q", formatted, wantQuote1)
	}

	// 3. Depth-3 quote unwrapped with ">>> " prefix (normalized from "> >> ")
	wantQuote2 := ">>> Can we drop the lock before allocating the new pages and re-acquire it only when splicing the list?"
	if !strings.Contains(formatted, wantQuote2) {
		t.Errorf("FormatEmailForReading() = \n%s\nwant substring %q", formatted, wantQuote2)
	}

	// 4. Unquoted prose unwrapped into a single sentence
	wantProse := "I tested this approach on a 64-core system under heavy trace workload and confirmed the latency spike disappears:"
	if !strings.Contains(formatted, wantProse) {
		t.Errorf("FormatEmailForReading() = \n%s\nwant substring %q", formatted, wantProse)
	}

	// 5. Indented code block preserved verbatim on separate lines
	wantCode := "  # trace-cmd record -e sched_wakeup\n  latency_max_us: 18"
	if !strings.Contains(formatted, wantCode) {
		t.Errorf("FormatEmailForReading() = \n%s\nwant substring %q", formatted, wantCode)
	}

	// 6. Multi-line bullet list item unwrapped while keeping separate bullets distinct
	wantBullet1 := "- Allocate pages outside buffer_mutex to avoid blocking readers during memory reclaim."
	wantBullet2 := "- Add a dedicated resize_lock mutex."
	if !strings.Contains(formatted, wantBullet1+"\n"+wantBullet2) {
		t.Errorf("formatted email did not unwrap bullet items properly;\ngot:\n%s", formatted)
	}

	// 7. Trailers and diff section preserved verbatim
	wantTrailers := "Signed-off-by: Kernel Developer <dev@example.com>\nReviewed-by: Reviewer One <reviewer@example.com>"
	if !strings.Contains(formatted, wantTrailers) {
		t.Errorf("formatted email did not preserve commit trailers;\ngot:\n%s", formatted)
	}
	if !strings.Contains(formatted, "-	old_call();\n+	new_call();") {
		t.Errorf("formatted email did not preserve diff lines;\ngot:\n%s", formatted)
	}
}

func TestTranslateEmail_SuccessAndStyleHandling(t *testing.T) {
	var receivedReqs []translateAPIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/translate" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req translateAPIRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		receivedReqs = append(receivedReqs, req)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"translated_text": "[JA:" + req.Text + "]",
			"model":           "plamo-3-translate-0.8b-Q4_K_M",
			"duration_ms":     12,
		})
	}))
	defer srv.Close()

	cfg := config.NewDefaultConfig()
	cfg.Translate.URL = srv.URL
	cfg.Translate.SourceLang = "en"
	cfg.Translate.TargetLang = "ja"
	cfg.Translate.Style = "polite"

	client := NewClient(cfg)

	rawBody := `Hello world, this is a line wrapped around eighty
columns for testing.

> Previous quoted line one
> and quoted line two.

Signed-off-by: Kernel Developer <dev@example.com>`

	res, err := client.TranslateEmail(context.Background(), "[PATCH v2 1/2] tracing: fix buffer lock", rawBody, Options{})
	if err != nil {
		t.Fatalf("TranslateEmail failed: %v", err)
	}

	if res.TranslatedSubject != "[PATCH v2 1/2] [JA:tracing: fix buffer lock]" {
		t.Errorf("TranslatedSubject = %q, want %q", res.TranslatedSubject, "[PATCH v2 1/2] [JA:tracing: fix buffer lock]")
	}

	wantBody := `[JA:Hello world, this is a line wrapped around eighty columns for testing.]

> [JA:Previous quoted line one and quoted line two.]

Signed-off-by: Kernel Developer <dev@example.com>`

	if res.TranslatedBody != wantBody {
		t.Errorf("TranslatedBody mismatch:\ngot:\n%s\nwant:\n%s", res.TranslatedBody, wantBody)
	}

	// Verify style="polite" was sent when target="ja"
	for _, req := range receivedReqs {
		if req.Style != "polite" {
			t.Errorf("req.Style = %q, want %q", req.Style, "polite")
		}
	}

	// Verify style is omitted when target != "ja" (per local-jp-translate API contract)
	receivedReqs = nil
	_, err = client.TranslateEmail(context.Background(), "Subject", "Test paragraph.", Options{
		TargetLang: "de",
		Style:      "polite",
	})
	if err != nil {
		t.Fatalf("TranslateEmail for target=de failed: %v", err)
	}
	for _, req := range receivedReqs {
		if req.Style != "" {
			t.Errorf("req.Style = %q, want %q", req.Style, "")
		}
	}
}

func TestTranslateEmail_ServerNotRunningReturnsError(t *testing.T) {
	// Allocate an ephemeral port and close it immediately to guarantee ECONNREFUSED
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	closedAddr := "http://" + ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("failed to close listener: %v", err)
	}

	cfg := config.NewDefaultConfig()
	cfg.Translate.URL = closedAddr
	client := NewClient(cfg)

	_, err = client.TranslateEmail(context.Background(), "Test subject", "Hello world.", Options{})
	if err == nil {
		t.Fatal("expected error when local translation server is not running, got nil")
	}
	if !errors.Is(err, ErrServerUnreachable) {
		t.Fatalf("expected error wrapping ErrServerUnreachable, got: %v", err)
	}
}

func TestTranslateEmail_ServerNotReadyReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code":    "not_ready",
				"message": "llama-server is still loading model",
			},
		})
	}))
	defer srv.Close()

	cfg := config.NewDefaultConfig()
	cfg.Translate.URL = srv.URL
	client := NewClient(cfg)

	_, err := client.TranslateEmail(context.Background(), "Subject", "Body text.", Options{})
	if err == nil {
		t.Fatal("expected error on 503 not_ready, got nil")
	}
	if !errors.Is(err, ErrServerUnreachable) {
		t.Fatalf("expected error wrapping ErrServerUnreachable on 503 not_ready, got: %v", err)
	}
}

func TestTranslateEmail_FIFOQueueNoInterleaving(t *testing.T) {
	var (
		mu          sync.Mutex
		order       []string
		firstStarted = make(chan struct{}, 1)
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req translateAPIRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		order = append(order, req.Text)
		if len(order) == 1 {
			select {
			case firstStarted <- struct{}{}:
			default:
			}
		}
		mu.Unlock()

		time.Sleep(15 * time.Millisecond)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"translated_text": "T:" + req.Text,
			"model":           "plamo-3-translate",
		})
	}))
	defer srv.Close()

	cfg := config.NewDefaultConfig()
	cfg.Translate.URL = srv.URL
	client := NewClient(cfg)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, err := client.TranslateEmail(context.Background(), "", "A1.\n\nA2.", Options{})
		if err != nil {
			t.Errorf("Email A TranslateEmail failed: %v", err)
		}
	}()

	<-firstStarted

	go func() {
		defer wg.Done()
		_, err := client.TranslateEmail(context.Background(), "", "B1.\n\nB2.", Options{})
		if err != nil {
			t.Errorf("Email B TranslateEmail failed: %v", err)
		}
	}()

	wg.Wait()

	wantOrder := "A1.|A2.|B1.|B2."
	gotOrder := strings.Join(order, "|")
	if gotOrder != wantOrder {
		t.Errorf("request execution order = %q, want %q (no interleaving)", gotOrder, wantOrder)
	}
}

