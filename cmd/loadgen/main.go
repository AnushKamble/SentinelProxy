package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sentinelproxy/sentinelproxy/bench"
)

type headerFlags []string

func (h *headerFlags) String() string {
	return strings.Join(*h, ", ")
}

func (h *headerFlags) Set(value string) error {
	*h = append(*h, value)
	return nil
}

func main() {
	targetURL := flag.String("url", "http://127.0.0.1:8080/healthz", "Target URL to benchmark")
	name := flag.String("name", "workload", "Name of the benchmark workload/condition")
	method := flag.String("method", "GET", "HTTP method")
	concurrency := flag.Int("c", 10, "Concurrent client workers")
	duration := flag.Duration("d", 5*time.Second, "Duration of the measurement phase")
	warmup := flag.Duration("warmup", 1*time.Second, "Warm-up phase duration before measurement")
	targetRPS := flag.Int("rate", 0, "Target request rate in req/s (0 = unlimited)")
	timeout := flag.Duration("timeout", 5*time.Second, "Per-request client timeout")
	runs := flag.Int("runs", 1, "Number of repeated benchmark runs")
	body := flag.String("body", "", "Request payload body")
	outputDir := flag.String("out", "results/benchmarks", "Output directory for JSON/CSV results")

	var headers headerFlags
	flag.Var(&headers, "header", "Custom header in 'Key: Value' format (can be specified multiple times)")
	flag.Parse()

	headerMap := make(map[string]string)
	for _, h := range headers {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) == 2 {
			headerMap[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}

	cfg := bench.WorkloadConfig{
		Name:           *name,
		TargetURL:      *targetURL,
		Method:         *method,
		Headers:        headerMap,
		Body:           *body,
		Concurrency:    *concurrency,
		Duration:       *duration,
		WarmupDuration: *warmup,
		TargetRPS:      *targetRPS,
		Timeout:        *timeout,
	}

	if err := os.MkdirAll(*outputDir, 0755); err != nil {
		log.Fatalf("failed to create output directory %s: %v", *outputDir, err)
	}

	runner := bench.NewRunner(cfg)
	var runStats []bench.RunStats

	fmt.Printf("===================================================================\n")
	fmt.Printf(" SentinelProxy Load Generator (Condition: %s)\n", *name)
	fmt.Printf(" Target: %s [%s] | Concurrency: %d | Duration: %v | Runs: %d\n",
		*targetURL, *method, *concurrency, *duration, *runs)
	fmt.Printf("===================================================================\n")

	for i := 1; i <= *runs; i++ {
		fmt.Printf("[Run %d/%d] Warming up (%v)... Running load (%v)...\n", i, *runs, *warmup, *duration)
		stats, err := runner.Execute(i)
		if err != nil {
			log.Fatalf("run %d failed: %v", i, err)
		}
		runStats = append(runStats, *stats)

		fmt.Printf("  -> Total Reqs: %d | Throughput: %.2f req/s | p50: %.2f ms | p95: %.2f ms | p99: %.2f ms\n",
			stats.TotalRequests, stats.ThroughputRPS, stats.LatencyP50Ms, stats.LatencyP95Ms, stats.LatencyP99Ms)
	}

	summary := bench.ComputeSummary(*name, runStats)

	fmt.Printf("\n-------------------------------------------------------------------\n")
	fmt.Printf(" Aggregated Summary across %d runs:\n", *runs)
	fmt.Printf("   Median Throughput: %.2f req/s (min: %.2f, max: %.2f)\n",
		summary.ThroughputMedian, summary.ThroughputMin, summary.ThroughputMax)
	fmt.Printf("   Median Latencies:  p50: %.2f ms | p95: %.2f ms | p99: %.2f ms\n",
		summary.LatencyP50Median, summary.LatencyP95Median, summary.LatencyP99Median)
	fmt.Printf("   Median Success:    %.2f%%\n", summary.SuccessRatePct)
	fmt.Printf("-------------------------------------------------------------------\n")

	// Export JSON
	jsonPath := filepath.Join(*outputDir, fmt.Sprintf("%s_summary.json", *name))
	if err := bench.ExportSummaryJSON(jsonPath, summary); err != nil {
		log.Printf("Warning: failed to export summary JSON: %v", err)
	} else {
		fmt.Printf("Exported summary JSON: %s\n", jsonPath)
	}

	// Export CSV
	csvPath := filepath.Join(*outputDir, fmt.Sprintf("%s_runs.csv", *name))
	if err := bench.ExportRunsCSV(csvPath, runStats); err != nil {
		log.Printf("Warning: failed to export runs CSV: %v", err)
	} else {
		fmt.Printf("Exported runs CSV: %s\n", csvPath)
	}
}
