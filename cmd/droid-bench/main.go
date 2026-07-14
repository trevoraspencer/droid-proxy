// Command droid-bench is the droid-proxy performance evaluation suite. It
// benchmarks any OpenAI/Anthropic-compatible endpoint (droid-proxy, a provider
// directly, or an alternative local bridge proxy), serves a deterministic mock
// upstream for controlled comparisons, and verifies the prompt-caching
// fidelity properties a proxy must preserve.
//
// See docs/BENCHMARKS.md for methodology and usage.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/trevoraspencer/droid-proxy/internal/bench/fidelity"
	"github.com/trevoraspencer/droid-proxy/internal/bench/harness"
	"github.com/trevoraspencer/droid-proxy/internal/bench/mockupstream"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "mock":
		runMock(os.Args[2:])
	case "run":
		runBench(os.Args[2:])
	case "cache-check":
		runCacheCheck(os.Args[2:])
	case "example-config":
		fmt.Print(harness.ExampleConfig)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `droid-bench — performance evaluation suite for droid-proxy

Subcommands:
  mock            Serve a deterministic mock model provider (OpenAI chat,
                  OpenAI Responses, Anthropic Messages) with configurable
                  latency, simulated prompt caching, and request capture.
  run             Run benchmark scenarios from a YAML config against one or
                  more targets and print a comparison report.
  cache-check     Verify prompt-caching fidelity properties of a proxy that
                  forwards to a droid-bench mock upstream.
  example-config  Print an example run config to stdout.

Run 'droid-bench <subcommand> -h' for flags.
`)
}

func signalContext() context.Context {
	ctx, _ := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	return ctx
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "droid-bench error: %v\n", err)
	os.Exit(1)
}

func runMock(args []string) {
	fs := flag.NewFlagSet("mock", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:18100", "listen address")
	ttft := fs.Duration("ttft", 5*time.Millisecond, "simulated model latency before the first byte")
	interChunk := fs.Duration("inter-chunk", 2*time.Millisecond, "simulated latency between stream chunks")
	chunks := fs.Int("chunks", 40, "content chunks per streamed response")
	simulateCache := fs.Bool("simulate-cache", true, "simulate provider prompt-prefix caching in usage counters")
	_ = fs.Parse(args)

	srv := mockupstream.New(mockupstream.Options{
		TTFT:                *ttft,
		InterChunkDelay:     *interChunk,
		StreamChunks:        *chunks,
		SimulatePromptCache: *simulateCache,
	})
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fatal(err)
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	fmt.Printf("droid-bench mock upstream listening on http://%s (ttft=%s inter-chunk=%s chunks=%d simulate-cache=%v)\n",
		ln.Addr(), *ttft, *interChunk, *chunks, *simulateCache)

	ctx := signalContext()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()
	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fatal(err)
	}
}

func runBench(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	configPath := fs.String("config", "", "bench config YAML (see 'droid-bench example-config')")
	jsonOut := fs.String("json", "", "write full report JSON to this file")
	mdOut := fs.String("md", "", "write markdown report to this file")
	repeat := fs.Int("repeat", 1, "interleaved repetitions of the full target matrix; >1 reports paired deltas with mean±sd")
	quiet := fs.Bool("quiet", false, "suppress progress output")
	_ = fs.Parse(args)

	if *configPath == "" {
		fatal(fmt.Errorf("-config is required (generate one with 'droid-bench example-config > bench.yaml')"))
	}
	cfg, err := harness.LoadConfig(*configPath)
	if err != nil {
		fatal(err)
	}
	runner := &harness.Runner{Config: *cfg, Repeat: *repeat}
	if !*quiet {
		runner.Log = os.Stderr
	}
	results, err := runner.Run(signalContext())
	if err != nil {
		fatal(err)
	}
	report := harness.BuildReport(results)
	report.WriteText(os.Stdout)
	if *jsonOut != "" {
		f, err := os.Create(*jsonOut)
		if err != nil {
			fatal(err)
		}
		defer func() { _ = f.Close() }()
		if err := report.WriteJSON(f); err != nil {
			fatal(err)
		}
		fmt.Printf("wrote JSON report: %s\n", *jsonOut)
	}
	if *mdOut != "" {
		f, err := os.Create(*mdOut)
		if err != nil {
			fatal(err)
		}
		defer func() { _ = f.Close() }()
		report.WriteMarkdown(f)
		fmt.Printf("wrote markdown report: %s\n", *mdOut)
	}
}

func runCacheCheck(args []string) {
	fs := flag.NewFlagSet("cache-check", flag.ExitOnError)
	proxy := fs.String("proxy", "http://127.0.0.1:8787", "base URL of the proxy under test")
	mock := fs.String("mock", "http://127.0.0.1:18100", "base URL of the droid-bench mock upstream the proxy forwards to")
	chatModel := fs.String("chat-model", "", "proxy model alias for the openai-chat passthrough path (empty skips)")
	anthropicModel := fs.String("anthropic-model", "", "proxy model alias for the native anthropic-messages path (empty skips)")
	anthropicXlat := fs.String("anthropic-translated-model", "", "proxy model alias for the anthropic→openai-chat translated path (empty skips)")
	clientKey := fs.String("client-key", "", "proxy client_auth API key, if enabled")
	repeats := fs.Int("repeats", 3, "repeat count for determinism checks")
	_ = fs.Parse(args)

	if *chatModel == "" && *anthropicModel == "" && *anthropicXlat == "" {
		fatal(fmt.Errorf("provide at least one of -chat-model, -anthropic-model, -anthropic-translated-model"))
	}
	results, err := fidelity.Run(signalContext(), fidelity.Options{
		ProxyBase:                *proxy,
		MockBase:                 *mock,
		ChatModel:                *chatModel,
		AnthropicModel:           *anthropicModel,
		AnthropicTranslatedModel: *anthropicXlat,
		ClientAPIKey:             *clientKey,
		Repeats:                  *repeats,
	})
	if err != nil {
		fatal(err)
	}
	fidelity.Print(os.Stdout, results)
	if !fidelity.Passed(results) {
		os.Exit(1)
	}
	fmt.Println("[fidelity] all checks passed")
}
