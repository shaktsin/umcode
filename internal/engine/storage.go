package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/work"
)

const storageRetentionKey = "storage.retention_days"

var defaultRetentionDays = protocol.RetentionDays{Raw: 30, Stale: 30, Blob: 90, Redacted: 7}

func validateStorageRetention(p protocol.RetentionDays) error {
	for name, days := range map[string]int{"raw": p.Raw, "stale": p.Stale, "blob": p.Blob, "redacted": p.Redacted} {
		if days < 1 || days > 3650 {
			return fmt.Errorf("%s retention must be a whole number of days from 1 to 3650", name)
		}
	}
	return nil
}

func (e *Engine) configuredStorageRetention() (protocol.RetentionDays, string, error) {
	p := defaultRetentionDays
	if e.Cfg == nil {
		return p, "default", nil
	}
	c := e.Cfg.Storage
	configured := c.RetentionRawDays != 0 || c.RetentionStaleDays != 0 || c.RetentionBlobDays != 0 || c.RetentionRedactedDays != 0
	for name, days := range map[string]int{"raw": c.RetentionRawDays, "stale": c.RetentionStaleDays, "blob": c.RetentionBlobDays, "redacted": c.RetentionRedactedDays} {
		if days < 0 || days > 3650 {
			return protocol.RetentionDays{}, "", fmt.Errorf("configured %s retention must be 0 or 1..3650 days", name)
		}
	}
	if c.RetentionRawDays > 0 {
		p.Raw = c.RetentionRawDays
	}
	if c.RetentionStaleDays > 0 {
		p.Stale = c.RetentionStaleDays
	}
	if c.RetentionBlobDays > 0 {
		p.Blob = c.RetentionBlobDays
	}
	if c.RetentionRedactedDays > 0 {
		p.Redacted = c.RetentionRedactedDays
	}
	source := "default"
	if configured {
		source = "config"
	}
	return p, source, nil
}

func (e *Engine) storageRetentionSettings(ctx context.Context) (protocol.RetentionDays, string, error) {
	var p protocol.RetentionDays
	err := e.Store.GetSetting(ctx, storageRetentionKey, &p)
	if err == nil {
		if err = validateStorageRetention(p); err != nil {
			return protocol.RetentionDays{}, "", fmt.Errorf("invalid saved storage retention: %w", err)
		}
		return p, "stored", nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return protocol.RetentionDays{}, "", err
	}
	return e.configuredStorageRetention()
}

func retentionFromProtocol(p protocol.RetentionDays) work.Retention {
	return work.RetentionFromDays(p.Raw, p.Stale, p.Blob, p.Redacted)
}

func (e *Engine) storageRetention(ctx context.Context) (work.Retention, error) {
	p, _, err := e.storageRetentionSettings(ctx)
	if err != nil {
		return work.Retention{}, err
	}
	return retentionFromProtocol(p), nil
}

func (e *Engine) StorageSettings(ctx context.Context) (protocol.StorageSettingsResult, error) {
	p, source, err := e.storageRetentionSettings(ctx)
	if err != nil {
		return protocol.StorageSettingsResult{}, err
	}
	stats, err := work.Stats(ctx, e.Store, retentionFromProtocol(p), time.Now().UTC())
	if err != nil {
		return protocol.StorageSettingsResult{}, err
	}
	return protocol.StorageSettingsResult{Retention: p, Source: source, Stats: stats}, nil
}

func (e *Engine) SetStorageRetention(ctx context.Context, p protocol.RetentionDays) (protocol.StorageSettingsResult, error) {
	if err := validateStorageRetention(p); err != nil {
		return protocol.StorageSettingsResult{}, err
	}
	if err := e.Store.SetSetting(ctx, storageRetentionKey, p); err != nil {
		return protocol.StorageSettingsResult{}, err
	}
	return e.StorageSettings(ctx)
}

func (e *Engine) CleanupStorage(ctx context.Context) (protocol.StorageCleanupResult, error) {
	if e.Work == nil || e.Work.Vault == nil {
		return protocol.StorageCleanupResult{}, errors.New("vault cleanup is unavailable")
	}
	p, _, err := e.storageRetentionSettings(ctx)
	if err != nil {
		return protocol.StorageCleanupResult{}, err
	}
	retention := retentionFromProtocol(p)
	report, err := work.RunGC(ctx, e.Store, e.Work.Vault, retention, time.Now().UTC())
	if err != nil {
		return protocol.StorageCleanupResult{}, err
	}
	stats, err := work.Stats(ctx, e.Store, retention, time.Now().UTC())
	if err != nil {
		return protocol.StorageCleanupResult{}, err
	}
	return protocol.StorageCleanupResult{Deleted: report.Deleted, FreedBytes: report.FreedBytes, Stats: stats}, nil
}
