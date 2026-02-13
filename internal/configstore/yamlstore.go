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
	AppID             string `yaml:"app_id"`
	ModelID           string `yaml:"model_id"`
	GPUsPerReplica    int    `yaml:"gpus_per_replica"`
	Priority          int    `yaml:"priority"`
	MinReplicas       int    `yaml:"min_replicas"`
	FailureDomainRule string `yaml:"failure_domain_rule"`
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

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create watcher: %w", err)
	}

	if err := watcher.Add(s.path); err != nil {
		watcher.Close()
		return nil, fmt.Errorf("watch file %s: %w", s.path, err)
	}

	ch := make(chan model.Application, 16)

	go func() {
		defer close(ch)
		defer watcher.Close()

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
					apps, err := s.parseFile()
					if err != nil {
						slog.Error("configstore: failed to parse config", "error", err)
						return
					}
					for _, app := range apps {
						select {
						case ch <- app:
						case <-ctx.Done():
							return
						}
					}
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

	return ch, nil
}

func (s *YAMLStore) parseFile() ([]model.Application, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var f yamlFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

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
		})
	}
	return apps, nil
}
