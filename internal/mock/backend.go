package mock

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// FaultConfig defines runtime fault injection settings for the mock backend.
type FaultConfig struct {
	DelayMs       int     `json:"delay_ms"`
	FailureRate   float64 `json:"failure_rate"`
	FailureStatus int     `json:"failure_status"`
	Hang          bool    `json:"hang"`
	HealthHealthy bool    `json:"health_healthy"`
	HealthStatus  int     `json:"health_status"`
	Seed          int64   `json:"seed"`
}

// Server is a test mock backend with programmatic fault injection.
type Server struct {
	ID        string
	mu        sync.RWMutex
	faults    FaultConfig
	rng       *rand.Rand
	reqCount  int64
	failCount int64
}

// NewServer creates a new mock server with default healthy behavior.
func NewServer(id string) *Server {
	return &Server{
		ID: id,
		faults: FaultConfig{
			DelayMs:       0,
			FailureRate:   0,
			FailureStatus: http.StatusInternalServerError,
			Hang:          false,
			HealthHealthy: true,
			HealthStatus:  http.StatusOK,
			Seed:          time.Now().UnixNano(),
		},
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// SetFaults updates fault injection parameters.
func (s *Server) SetFaults(cfg FaultConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg.FailureStatus == 0 {
		cfg.FailureStatus = http.StatusInternalServerError
	}
	if cfg.HealthStatus == 0 {
		if cfg.HealthHealthy {
			cfg.HealthStatus = http.StatusOK
		} else {
			cfg.HealthStatus = http.StatusServiceUnavailable
		}
	}
	s.faults = cfg
	if cfg.Seed != 0 {
		s.rng = rand.New(rand.NewSource(cfg.Seed))
	}
}

// Reset clears all injected faults to normal healthy operation.
func (s *Server) Reset() {
	s.SetFaults(FaultConfig{
		DelayMs:       0,
		FailureRate:   0,
		FailureStatus: http.StatusInternalServerError,
		Hang:          false,
		HealthHealthy: true,
		HealthStatus:  http.StatusOK,
		Seed:          1,
	})
}

// GetFaults returns current fault configuration.
func (s *Server) GetFaults() FaultConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.faults
}

// Handler returns an http.Handler implementing mock backend endpoints.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/-/faults", s.handleFaults)
	mux.HandleFunc("/-/reset", s.handleReset)
	mux.HandleFunc("/stream", s.handleStream)
	mux.HandleFunc("/", s.handleEcho)
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	cfg := s.faults
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(cfg.HealthStatus)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"backend_id": s.ID,
		"status":     cfg.HealthStatus,
		"healthy":    cfg.HealthHealthy,
	})
}

func (s *Server) handleFaults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var cfg FaultConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, fmt.Sprintf("invalid json: %v", err), http.StatusBadRequest)
		return
	}
	s.SetFaults(cfg)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "faults updated"})
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	s.Reset()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "reset to healthy"})
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	// Stream back chunks to test streaming proxying
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Flushing not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)

	chunks := 5
	if cStr := r.URL.Query().Get("chunks"); cStr != "" {
		if c, err := strconv.Atoi(cStr); err == nil && c > 0 {
			chunks = c
		}
	}

	for i := 0; i < chunks; i++ {
		_, _ = fmt.Fprintf(w, "chunk-%d\n", i)
		flusher.Flush()
		time.Sleep(10 * time.Millisecond)
	}
}

func (s *Server) handleEcho(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.reqCount++
	cfg := s.faults

	// Check per-request query overrides if provided
	if dStr := r.URL.Query().Get("delay_ms"); dStr != "" {
		if d, err := strconv.Atoi(dStr); err == nil && d >= 0 {
			cfg.DelayMs = d
		}
	}
	if sStr := r.URL.Query().Get("status"); sStr != "" {
		if st, err := strconv.Atoi(sStr); err == nil && st >= 100 && st <= 599 {
			cfg.FailureRate = 1.0
			cfg.FailureStatus = st
		}
	}
	if r.URL.Query().Get("hang") == "true" {
		cfg.Hang = true
	}

	shouldFail := false
	if cfg.FailureRate > 0 {
		if cfg.FailureRate >= 1.0 || s.rng.Float64() < cfg.FailureRate {
			shouldFail = true
			s.failCount++
		}
	}
	s.mu.Unlock()

	if cfg.Hang {
		// Hang until client context cancels or long timeout
		select {
		case <-r.Context().Done():
			return
		case <-time.After(30 * time.Second):
			http.Error(w, "hung timeout", http.StatusGatewayTimeout)
			return
		}
	}

	if cfg.DelayMs > 0 {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Duration(cfg.DelayMs) * time.Millisecond):
		}
	}

	if shouldFail {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Backend-ID", s.ID)
		w.WriteHeader(cfg.FailureStatus)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"backend_id": s.ID,
			"error":      "injected failure",
			"status":     cfg.FailureStatus,
		})
		return
	}

	// Read request body to echo it
	bodyBytes, _ := io.ReadAll(r.Body)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Backend-ID", s.ID)
	w.Header().Set("X-Echo-Method", r.Method)
	w.Header().Set("X-Echo-Path", r.URL.Path)

	resp := map[string]any{
		"backend_id": s.ID,
		"method":     r.Method,
		"path":       r.URL.Path,
		"query":      r.URL.RawQuery,
		"headers":    r.Header,
		"body":       string(bodyBytes),
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
