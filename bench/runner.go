package bench

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// WorkloadConfig configures a load generation run.
type WorkloadConfig struct {
	Name           string            `json:"name"`
	TargetURL      string            `json:"target_url"`
	Method         string            `json:"method"`
	Headers        map[string]string `json:"headers,omitempty"`
	Body           string            `json:"body,omitempty"`
	Concurrency    int               `json:"concurrency"`
	Duration       time.Duration     `json:"duration"`
	WarmupDuration time.Duration     `json:"warmup_duration"`
	TargetRPS      int               `json:"target_rps"` // 0 = unlimited throughput
	Timeout        time.Duration     `json:"timeout"`
}

// RequestRecord stores metrics for an individual request.
type RequestRecord struct {
	Timestamp  time.Time     `json:"timestamp"`
	StatusCode int           `json:"status_code"`
	Duration   time.Duration `json:"duration"`
	Error      string        `json:"error,omitempty"`
}

// RunStats summarizes a completed benchmark run.
type RunStats struct {
	RunIndex           int              `json:"run_index"`
	Name               string           `json:"name"`
	TargetURL          string           `json:"target_url"`
	Concurrency        int              `json:"concurrency"`
	ActualDurationMs   int64            `json:"actual_duration_ms"`
	TotalRequests      int64            `json:"total_requests"`
	SuccessRequests    int64            `json:"success_requests"`
	FailureRequests    int64            `json:"failure_requests"`
	ThroughputRPS      float64          `json:"throughput_rps"`
	StatusDistribution map[int]int64    `json:"status_distribution"`
	LatencyP50Ms       float64          `json:"latency_p50_ms"`
	LatencyP90Ms       float64          `json:"latency_p90_ms"`
	LatencyP95Ms       float64          `json:"latency_p95_ms"`
	LatencyP99Ms       float64          `json:"latency_p99_ms"`
	LatencyMinMs       float64          `json:"latency_min_ms"`
	LatencyMaxMs       float64          `json:"latency_max_ms"`
	LatencyMeanMs      float64          `json:"latency_mean_ms"`
	Records            []RequestRecord  `json:"-"` // omit detailed raw records in summary json
}

// ExperimentSummary aggregates statistics across multiple repeated runs of a condition.
type ExperimentSummary struct {
	ConditionName    string     `json:"condition_name"`
	NumRuns          int        `json:"num_runs"`
	ThroughputMedian float64    `json:"throughput_median_rps"`
	ThroughputMin    float64    `json:"throughput_min_rps"`
	ThroughputMax    float64    `json:"throughput_max_rps"`
	LatencyP50Median float64    `json:"latency_p50_median_ms"`
	LatencyP95Median float64    `json:"latency_p95_median_ms"`
	LatencyP99Median float64    `json:"latency_p99_median_ms"`
	SuccessRatePct   float64    `json:"success_rate_median_pct"`
	Runs             []RunStats `json:"runs"`
}

// Runner executes load tests against HTTP endpoints.
type Runner struct {
	cfg    WorkloadConfig
	client *http.Client
}

// NewRunner creates a new workload generator runner.
func NewRunner(cfg WorkloadConfig) *Runner {
	if cfg.Method == "" {
		cfg.Method = http.MethodGet
	}
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.Duration <= 0 {
		cfg.Duration = 5 * time.Second
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}

	transport := &http.Transport{
		MaxIdleConns:        1000,
		MaxIdleConnsPerHost: 200,
		MaxConnsPerHost:     500,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
	}

	return &Runner{
		cfg: cfg,
		client: &http.Client{
			Transport: transport,
			Timeout:   cfg.Timeout,
		},
	}
}

// Execute performs warmup (if configured) followed by the timed benchmark measurement run.
func (r *Runner) Execute(runIndex int) (*RunStats, error) {
	// Warm-up phase
	if r.cfg.WarmupDuration > 0 {
		_ = r.runLoad(r.cfg.WarmupDuration, false)
	}

	// Measurement phase
	stats := r.runLoad(r.cfg.Duration, true)
	stats.RunIndex = runIndex
	stats.Name = r.cfg.Name
	stats.TargetURL = r.cfg.TargetURL
	stats.Concurrency = r.cfg.Concurrency
	return stats, nil
}

func (r *Runner) runLoad(duration time.Duration, recordDetails bool) *RunStats {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var totalReqs int64
	var successReqs int64
	var failReqs int64
	statusDist := make(map[int]*int64)

	var mu sync.Mutex
	var allRecords []RequestRecord

	// Rate limiter ticker if TargetRPS is set
	var rateTicker <-chan time.Time
	if r.cfg.TargetRPS > 0 {
		interval := time.Second / time.Duration(r.cfg.TargetRPS)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		rateTicker = ticker.C
	}

	var wg sync.WaitGroup
	startTime := time.Now()

	for workerID := 0; workerID < r.cfg.Concurrency; workerID++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var localRecords []RequestRecord

			for {
				select {
				case <-ctx.Done():
					if recordDetails && len(localRecords) > 0 {
						mu.Lock()
						allRecords = append(allRecords, localRecords...)
						mu.Unlock()
					}
					return
				default:
				}

				if rateTicker != nil {
					select {
					case <-ctx.Done():
						return
					case <-rateTicker:
					}
				}

				rec := r.sendOneRequest()
				atomic.AddInt64(&totalReqs, 1)

				if rec.StatusCode >= 200 && rec.StatusCode < 400 {
					atomic.AddInt64(&successReqs, 1)
				} else {
					atomic.AddInt64(&failReqs, 1)
				}

				mu.Lock()
				if _, ok := statusDist[rec.StatusCode]; !ok {
					var c int64
					statusDist[rec.StatusCode] = &c
				}
				atomic.AddInt64(statusDist[rec.StatusCode], 1)
				mu.Unlock()

				if recordDetails {
					localRecords = append(localRecords, rec)
				}
			}
		}()
	}

	wg.Wait()
	actualDuration := time.Since(startTime)

	// Consolidate status distribution
	finalStatusDist := make(map[int]int64)
	for code, countPtr := range statusDist {
		finalStatusDist[code] = atomic.LoadInt64(countPtr)
	}

	// Compute latencies
	var latencies []time.Duration
	var latSum float64
	for _, rec := range allRecords {
		latencies = append(latencies, rec.Duration)
		latSum += float64(rec.Duration.Microseconds()) / 1000.0
	}

	p50, p90, p95, p99, minMs, maxMs := calculatePercentiles(latencies)
	var meanMs float64
	if len(latencies) > 0 {
		meanMs = latSum / float64(len(latencies))
	}

	rps := 0.0
	if actualDuration.Seconds() > 0 {
		rps = float64(totalReqs) / actualDuration.Seconds()
	}

	return &RunStats{
		ActualDurationMs:   actualDuration.Milliseconds(),
		TotalRequests:      totalReqs,
		SuccessRequests:    successReqs,
		FailureRequests:    failReqs,
		ThroughputRPS:      rps,
		StatusDistribution: finalStatusDist,
		LatencyP50Ms:       p50,
		LatencyP90Ms:       p90,
		LatencyP95Ms:       p95,
		LatencyP99Ms:       p99,
		LatencyMinMs:       minMs,
		LatencyMaxMs:       maxMs,
		LatencyMeanMs:      meanMs,
		Records:            allRecords,
	}
}

func (r *Runner) sendOneRequest() RequestRecord {
	var bodyReader io.Reader
	if len(r.cfg.Body) > 0 {
		bodyReader = bytes.NewBufferString(r.cfg.Body)
	}

	req, err := http.NewRequest(r.cfg.Method, r.cfg.TargetURL, bodyReader)
	if err != nil {
		return RequestRecord{
			Timestamp:  time.Now(),
			StatusCode: 0,
			Duration:   0,
			Error:      err.Error(),
		}
	}

	for k, v := range r.cfg.Headers {
		req.Header.Set(k, v)
	}

	reqStart := time.Now()
	resp, err := r.client.Do(req)
	duration := time.Since(reqStart)

	if err != nil {
		return RequestRecord{
			Timestamp:  reqStart,
			StatusCode: 0,
			Duration:   duration,
			Error:      err.Error(),
		}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	return RequestRecord{
		Timestamp:  reqStart,
		StatusCode: resp.StatusCode,
		Duration:   duration,
	}
}

func calculatePercentiles(durations []time.Duration) (p50, p90, p95, p99, minMs, maxMs float64) {
	if len(durations) == 0 {
		return 0, 0, 0, 0, 0, 0
	}
	sort.Slice(durations, func(i, j int) bool {
		return durations[i] < durations[j]
	})

	n := len(durations)
	minMs = float64(durations[0].Microseconds()) / 1000.0
	maxMs = float64(durations[n-1].Microseconds()) / 1000.0
	p50 = float64(durations[n*50/100].Microseconds()) / 1000.0
	p90 = float64(durations[n*90/100].Microseconds()) / 1000.0
	p95 = float64(durations[n*95/100].Microseconds()) / 1000.0
	p99 = float64(durations[n*99/100].Microseconds()) / 1000.0
	return
}

// ComputeSummary aggregates multiple runs into an ExperimentSummary.
func ComputeSummary(conditionName string, runs []RunStats) ExperimentSummary {
	if len(runs) == 0 {
		return ExperimentSummary{ConditionName: conditionName}
	}

	var rpsList []float64
	var p50List []float64
	var p95List []float64
	var p99List []float64
	var succRateList []float64

	minRPS := math.MaxFloat64
	maxRPS := 0.0

	for _, run := range runs {
		rpsList = append(rpsList, run.ThroughputRPS)
		if run.ThroughputRPS < minRPS {
			minRPS = run.ThroughputRPS
		}
		if run.ThroughputRPS > maxRPS {
			maxRPS = run.ThroughputRPS
		}

		p50List = append(p50List, run.LatencyP50Ms)
		p95List = append(p95List, run.LatencyP95Ms)
		p99List = append(p99List, run.LatencyP99Ms)

		var succPct float64
		if run.TotalRequests > 0 {
			succPct = float64(run.SuccessRequests) / float64(run.TotalRequests) * 100.0
		}
		succRateList = append(succRateList, succPct)
	}

	sort.Float64s(rpsList)
	sort.Float64s(p50List)
	sort.Float64s(p95List)
	sort.Float64s(p99List)
	sort.Float64s(succRateList)

	mid := len(runs) / 2

	return ExperimentSummary{
		ConditionName:    conditionName,
		NumRuns:          len(runs),
		ThroughputMedian: rpsList[mid],
		ThroughputMin:    minRPS,
		ThroughputMax:    maxRPS,
		LatencyP50Median: p50List[mid],
		LatencyP95Median: p95List[mid],
		LatencyP99Median: p99List[mid],
		SuccessRatePct:   succRateList[mid],
		Runs:             runs,
	}
}

// ExportSummaryJSON writes an ExperimentSummary to a JSON file.
func ExportSummaryJSON(path string, summary ExperimentSummary) error {
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// ExportRunsCSV writes per-run statistics to a CSV file.
func ExportRunsCSV(path string, runs []RunStats) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	header := []string{
		"run_index", "condition", "target_url", "concurrency",
		"duration_ms", "total_requests", "success_requests", "failure_requests",
		"throughput_rps", "p50_ms", "p90_ms", "p95_ms", "p99_ms", "min_ms", "max_ms", "mean_ms",
	}
	if err := w.Write(header); err != nil {
		return err
	}

	for _, r := range runs {
		row := []string{
			strconv.Itoa(r.RunIndex),
			r.Name,
			r.TargetURL,
			strconv.Itoa(r.Concurrency),
			strconv.FormatInt(r.ActualDurationMs, 10),
			strconv.FormatInt(r.TotalRequests, 10),
			strconv.FormatInt(r.SuccessRequests, 10),
			strconv.FormatInt(r.FailureRequests, 10),
			fmt.Sprintf("%.2f", r.ThroughputRPS),
			fmt.Sprintf("%.2f", r.LatencyP50Ms),
			fmt.Sprintf("%.2f", r.LatencyP90Ms),
			fmt.Sprintf("%.2f", r.LatencyP95Ms),
			fmt.Sprintf("%.2f", r.LatencyP99Ms),
			fmt.Sprintf("%.2f", r.LatencyMinMs),
			fmt.Sprintf("%.2f", r.LatencyMaxMs),
			fmt.Sprintf("%.2f", r.LatencyMeanMs),
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}

	return nil
}
