package configstore

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"
	"gopkg.in/yaml.v3"

	"github.com/fsnotify/fsnotify"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

const debounceWindow = 500 * time.Millisecond

// yamlFile is the on-disk representation of the config file.
type yamlFile struct {
	Applications []yamlApp `yaml:"applications"`
}

type yamlApp struct {
	AppID             string      `yaml:"app_id"`
	ModelID           string      `yaml:"model_id"`
	GPUsPerReplica    int         `yaml:"gpus_per_replica"`
	Priority          int         `yaml:"priority"`
	MinReplicas       int         `yaml:"min_replicas"`
	FailureDomainRule string      `yaml:"failure_domain_rule"`
	SLA               yamlSLA     `yaml:"sla"`
	Metrics           yamlMetrics `yaml:"metrics"`
}

type yamlSLA struct {
	MaxP99TTFTMs int `yaml:"max_p99_ttft_ms"`
	MaxP99TPS    int `yaml:"max_p99_tps"`
}

type yamlMetrics struct {
	AvgWaitingQueueDepth  float64 `yaml:"avg_waiting_queue_depth"`
	MaxWaitingQueueDepth  float64 `yaml:"max_waiting_queue_depth"`
	AvgRunningQueueDepth  float64 `yaml:"avg_running_queue_depth"`
	AvgBatchUtilization   float64 `yaml:"avg_batch_utilization"`
	AvgKVCacheUtilization float64 `yaml:"avg_kv_cache_utilization"`
	MaxKVCacheUtilization float64 `yaml:"max_kv_cache_utilization"`
	TotalTokensPerSecond  float64 `yaml:"total_tokens_per_second"`
	AvgTimeToFirstTokenMs float64 `yaml:"avg_time_to_first_token_ms"`
	P99TimeToFirstTokenMs float64 `yaml:"p99_time_to_first_token_ms"`
}

// YAMLStore implements ports.ConfigStore by reading a YAML file.
type YAMLStore struct {
	path   string
	tracer trace.Tracer
}

// NewYAMLStore creates a YAMLStore for the given file path.
func NewYAMLStore(path string, tracer trace.Tracer) *YAMLStore {
	return &YAMLStore{
		path:   path,
		tracer: tracer,
	}
}

// ListApplications reads and parses the YAML file.
func (s *YAMLStore) ListApplications(ctx context.Context) ([]model.Application, error) {
	_, span := s.tracer.Start(ctx, "configstore.list_applications")
	defer span.End()

	return s.parseFile()
}

// WatchApplications watches the YAML file for changes using fsnotify and emits
// new/updated applications. Debounces with 500ms window.
func (s *YAMLStore) WatchApplications(ctx context.Context) (<-chan model.Application, error) {
	_, span := s.tracer.Start(ctx, "configstore.watch_applications")
	defer span.End()

	ch := make(chan model.Application, 16)
	s.startWatcher(ctx, ch, func(f yamlFile) {
		apps, _ := s.convertApps(f)
		for _, app := range apps {
			select {
			case ch <- app:
			case <-ctx.Done():
				return
			}
		}
	})
	return ch, nil
}

// WatchMetrics watches the YAML file for changes and emits updated vLLM metrics.
func (s *YAMLStore) WatchMetrics(ctx context.Context) (<-chan model.VLLMMetrics, error) {
	_, span := s.tracer.Start(ctx, "configstore.watch_metrics")
	defer span.End()

	ch := make(chan model.VLLMMetrics, 16)

	// Emit initial metrics immediately.
	if f, err := s.readFile(); err == nil {
		for _, a := range f.Applications {
			ch <- model.VLLMMetrics{
				AppID:                 a.AppID,
				AvgWaitingQueueDepth:  a.Metrics.AvgWaitingQueueDepth,
				MaxWaitingQueueDepth:  a.Metrics.MaxWaitingQueueDepth,
				AvgRunningQueueDepth:  a.Metrics.AvgRunningQueueDepth,
				AvgBatchUtilization:   a.Metrics.AvgBatchUtilization,
				AvgKVCacheUtilization: a.Metrics.AvgKVCacheUtilization,
				MaxKVCacheUtilization: a.Metrics.MaxKVCacheUtilization,
				TotalTokensPerSecond:  a.Metrics.TotalTokensPerSecond,
				AvgTimeToFirstTokenMs: a.Metrics.AvgTimeToFirstTokenMs,
				P99TimeToFirstTokenMs: a.Metrics.P99TimeToFirstTokenMs,
				MeasuredAt:            time.Now(),
			}
		}
	}

	s.startWatcher(ctx, ch, func(f yamlFile) {
		for _, a := range f.Applications {
			m := model.VLLMMetrics{
				AppID:                 a.AppID,
				AvgWaitingQueueDepth:  a.Metrics.AvgWaitingQueueDepth,
				MaxWaitingQueueDepth:  a.Metrics.MaxWaitingQueueDepth,
				AvgRunningQueueDepth:  a.Metrics.AvgRunningQueueDepth,
				AvgBatchUtilization:   a.Metrics.AvgBatchUtilization,
				AvgKVCacheUtilization: a.Metrics.AvgKVCacheUtilization,
				MaxKVCacheUtilization: a.Metrics.MaxKVCacheUtilization,
				TotalTokensPerSecond:  a.Metrics.TotalTokensPerSecond,
				AvgTimeToFirstTokenMs: a.Metrics.AvgTimeToFirstTokenMs,
				P99TimeToFirstTokenMs: a.Metrics.P99TimeToFirstTokenMs,
				MeasuredAt:            time.Now(),
			}
			select {
			case ch <- m:
			case <-ctx.Done():
				return
			}
		}
	})
	return ch, nil
}

func (s *YAMLStore) startWatcher(ctx context.Context, outCh interface{}, onUpdate func(yamlFile)) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("configstore: failed to create watcher", "error", err)
		return
	}

	if err := watcher.Add(s.path); err != nil {
		watcher.Close()
		slog.Error("configstore: failed to watch file", "path", s.path, "error", err)
		return
	}

	go func() {
		defer watcher.Close()
		defer func() {
			// Close the channel using reflection to handle both Application and VLLMMetrics channels.
			switch c := outCh.(type) {
			case chan model.Application:
				close(c)
			case chan model.VLLMMetrics:
				close(c)
			}
		}()

		var mu sync.Mutex
		var debounceTimer *time.Timer

		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
					continue
				}

				mu.Lock()
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				debounceTimer = time.AfterFunc(debounceWindow, func() {
					f, err := s.readFile()
					if err != nil {
						slog.Error("configstore: failed to read config", "error", err)
						return
					}
					onUpdate(f)
				})
				mu.Unlock()

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				slog.Error("configstore: watcher error", "error", err)
			}
		}
	}()
}

func (s *YAMLStore) readFile() (yamlFile, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return yamlFile{}, err
	}
	var f yamlFile
	err = yaml.Unmarshal(data, &f)
	return f, err
}

func (s *YAMLStore) convertApps(f yamlFile) ([]model.Application, error) {
	apps := make([]model.Application, 0, len(f.Applications))
	for _, a := range f.Applications {
		rule := model.FailureDomainNone
		if a.FailureDomainRule == "spread_clusters" {
			rule = model.FailureDomainSpreadClusters
		}
		apps = append(apps, model.Application{
			AppID:             a.AppID,
			ModelID:           a.ModelID,
			GPUsPerReplica:    a.GPUsPerReplica,
			Priority:          a.Priority,
			MinReplicas:       a.MinReplicas,
			FailureDomainRule: rule,
			SLA: model.SLA{
				AppID:        a.AppID,
				MaxP99TTFTMs: a.SLA.MaxP99TTFTMs,
				MaxP99TPS:    a.SLA.MaxP99TPS,
			},
		})
	}
	return apps, nil
}

func (s *YAMLStore) parseFile() ([]model.Application, error) {
	f, err := s.readFile()
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	return s.convertApps(f)
}
