package bot

import (
	"context"
	"log"
	"path/filepath"

	"github.com/eastlaugh/gobot/internal/config"
	"github.com/eastlaugh/gobot/internal/session"
	"github.com/fsnotify/fsnotify"
)

func watchConfig(ctx context.Context, path string, changed chan<- struct{}) (*fsnotify.Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	if err := watcher.Add(dir); err != nil {
		_ = watcher.Close()
		return nil, err
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if filepath.Base(event.Name) != name {
					continue
				}
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) {
					select {
					case changed <- struct{}{}:
					default:
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("watch go.toml failed: %v", err)
			}
		}
	}()
	return watcher, nil
}


func reloadGroups(groups map[int64]*session.Group, cfg *config.Config, stopTimer func(*session.Group), finalObserve func(*session.Group), pokeMaintainer func(int64, int64)) {
	next := session.NewGroups(cfg)
	for id, group := range groups {
		if next[id] != nil {
			continue
		}
		stopTimer(group)
		if !group.Runtime.Observing {
			finalObserve(group)
		}
		if group.Runtime.ObserveCancel != nil {
			group.Runtime.ObserveCancel()
		}
		delete(groups, id)
		log.Printf("群 %d 已从 go.toml 移除", id)
	}
	for id, nextGroup := range next {
		group := groups[id]
		if group == nil {
			groups[id] = nextGroup
			log.Printf("群 %d 已加入 go.toml", id)
			continue
		}
		promptChanged := group.Prompt != nextGroup.Prompt
		memoryChanged := group.MemoryFile != nextGroup.MemoryFile
		if promptChanged || memoryChanged {
			stopTimer(group)
			if !group.Runtime.Observing {
				finalObserve(group)
			}
			if group.Runtime.ObserveCancel != nil {
				group.Runtime.ObserveCancel()
			}
			group.Prompt = nextGroup.Prompt
			group.MemoryFile = nextGroup.MemoryFile
			group.Runtime = session.Runtime{Generation: group.Runtime.Generation + 1}
			log.Printf("群 %d 配置已变更，已重启当前对话", id)
			for _, qq := range cfg.Maintainers {
				pokeMaintainer(id, qq)
			}
		}
	}
}
