package config

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ReloadCallback is the function signature executed when a valid config change is detected.
type ReloadCallback func() error

// Watcher monitors a configuration file on disk and triggers debounced reloads.
type Watcher struct {
	filePath         string
	callback         ReloadCallback
	logger           *slog.Logger
	debounceDuration time.Duration
	watcher          *fsnotify.Watcher
	mu               sync.Mutex
	stopChan         chan struct{}
	wg               sync.WaitGroup
}

// NewWatcher creates a file system watcher for the specified config path.
func NewWatcher(filePath string, callback ReloadCallback, logger *slog.Logger) (*Watcher, error) {
	if logger == nil {
		logger = slog.Default()
	}

	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve absolute config path: %w", err)
	}

	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}

	return &Watcher{
		filePath:         absPath,
		callback:         callback,
		logger:           logger,
		debounceDuration: 75 * time.Millisecond,
		watcher:          fsWatcher,
		stopChan:         make(chan struct{}),
	}, nil
}

// Start begins watching the config file directory to catch write, create, and rename events.
func (w *Watcher) Start(ctx context.Context) error {
	dir := filepath.Dir(w.filePath)
	if err := w.watcher.Add(dir); err != nil {
		return fmt.Errorf("failed to add config directory to watcher: %w", err)
	}

	w.wg.Add(1)
	go w.watchLoop(ctx)
	return nil
}

func (w *Watcher) watchLoop(ctx context.Context) {
	defer w.wg.Done()

	var debounceTimer *time.Timer
	var debounceChan <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopChan:
			return

		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}

			// Normalize event path
			eventClean := filepath.Clean(event.Name)
			targetClean := filepath.Clean(w.filePath)

			if eventClean == targetClean {
				// Handle write, create, or rename (atomic file replacement in editors)
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) {
					w.logger.Debug("config file modification event detected", "event", event.String())
					if debounceTimer != nil {
						debounceTimer.Stop()
					}
					debounceTimer = time.NewTimer(w.debounceDuration)
					debounceChan = debounceTimer.C
				}
			}

		case <-debounceChan:
			w.logger.Info("triggering config reload after file modification", "path", w.filePath)
			if err := w.callback(); err != nil {
				w.logger.Error("config reload failed", "error", err)
			} else {
				w.logger.Info("config reload completed successfully")
			}
			debounceChan = nil

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			w.logger.Warn("file watcher encountered error", "error", err)
		}
	}
}

// Stop cleanly terminates the file watcher.
func (w *Watcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()

	select {
	case <-w.stopChan:
		return
	default:
		close(w.stopChan)
	}

	_ = w.watcher.Close()
	w.wg.Wait()
}
