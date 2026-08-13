// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"sync"
	"time"
)

// metaRefreshInterval bounds how stale a meta doc's lastSeenAt can get while a
// metric keeps reporting with a stable label set. Without a periodic refresh,
// lastSeenAt would freeze at the moment the label set last changed, and
// CleanStaleMetadata (Phase 4) would wrongly prune actively-reporting metrics.
// One upsert per metric per interval is negligible write amplification.
const metaRefreshInterval = 5 * time.Minute

// metaCache deduplicates metadata upserts within the process lifetime. Writing
// every data point's metadata on every write would amplify write volume 1-2
// orders of magnitude for no benefit: the meta doc only changes when a metric
// gains a label key it has never carried before (or is first seen).
//
// The cache maps docID ({appID}:{name}) → {label keys already pushed, last
// upsert time}. An upsert is needed when the incoming doc carries a new label
// key, the docID is new, or the last upsert exceeded metaRefreshInterval (to
// refresh lastSeenAt). It is deliberately process-local and lossy: on restart
// (or across replicas) the cache is empty, so the first write of each metric
// re-upserts — the Painless script unions the existing ES doc's labelKeys, so
// no data is lost, only a redundant write.
type metaCache struct {
	mu   sync.Mutex
	seen map[string]*metaCacheEntry
}

type metaCacheEntry struct {
	keys       map[string]struct{}
	lastUpsert time.Time
}

// newMetaCache creates an empty metaCache.
func newMetaCache() *metaCache {
	return &metaCache{seen: make(map[string]*metaCacheEntry)}
}

// shouldUpsert reports whether the given meta doc warrants an upsert: true on
// first sight of a docID, when the doc carries a label key not yet recorded for
// that docID, or when the last upsert for that docID exceeded metaRefreshInterval
// (lastSeenAt refresh). It records the doc's label keys on the way out.
//
// It is safe for concurrent use. The caller still issues the upsert
// independently; a false positive here only causes a redundant (idempotent)
// upsert, never a missed one.
func (c *metaCache) shouldUpsert(doc MetaDoc, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := metaDocID(doc.AppID, doc.Name)
	entry, ok := c.seen[id]
	if !ok {
		entry = &metaCacheEntry{keys: make(map[string]struct{}, len(doc.LabelKeys))}
		c.seen[id] = entry
	}
	hasNew := false
	for _, k := range doc.LabelKeys {
		if _, seen := entry.keys[k]; !seen {
			entry.keys[k] = struct{}{}
			hasNew = true
		}
	}
	stale := now.Sub(entry.lastUpsert) >= metaRefreshInterval
	if hasNew || !ok || stale {
		entry.lastUpsert = now
		return true
	}
	return false
}
