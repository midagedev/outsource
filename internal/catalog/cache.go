package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// CacheDir is where catalogues are kept:
// ${XDG_CACHE_HOME:-$HOME/.cache}/outsource/catalog. An empty XDG_CACHE_HOME
// counts as unset, as it does for internal/slot. The dispatcher's binary
// cache lives in version-named siblings, and its prune (bin/outsource
// prune_cache) only touches names made of digits and dots, so catalog/
// survives an update.
//
// Files: <provider>.json per catalogue, openrouter-providers.json for the
// policy list, openrouter-endpoints.json for the per-id endpoint lists.
func CacheDir() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME")
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "outsource", "catalog")
}

// cacheFile is one cached response. The raw body is kept rather than the
// parsed entries, so a parser change applies to a cache written before it.
type cacheFile struct {
	FetchedAt time.Time `json:"fetched_at"`
	Source    string    `json:"source"` // the URL or command it came from, for a human reading the file
	Body      string    `json:"body"`
}

func cachePath(name string) string { return filepath.Join(CacheDir(), name+".json") }

// isFresh is the one freshness rule. A stamp in the future (a clock that
// moved back, a file from another machine) is not fresh: it would otherwise
// stay "fresh" for as long as the skew lasts.
func isFresh(at, now time.Time) bool {
	age := now.Sub(at)
	return age >= 0 && age < FreshFor
}

// loadCached is the cache policy every catalogue response goes through:
//
//  1. a fresh cache that parses is used without a request (unless refresh);
//  2. otherwise fetch, parse, and only then write the cache — a 200 carrying
//     an error page must not poison the cache for an hour;
//  3. a failed fetch or parse falls back to any cache that parses, stale or
//     not, and reports the failure beside it;
//  4. with no usable cache either, the source carries the error alone.
func loadCached[T any](name, source string, refresh bool, fetch func() ([]byte, error), parse func([]byte) (T, error)) (T, Source) {
	var zero T
	now := time.Now()
	cached, haveCache := readCache(name)
	var fromCache T
	if haveCache {
		v, err := parse([]byte(cached.Body))
		if err != nil {
			haveCache = false
		} else {
			fromCache = v
		}
	}
	if haveCache && !refresh && isFresh(cached.FetchedAt, now) {
		return fromCache, Source{From: FromCache, FetchedAt: cached.FetchedAt}
	}

	body, err := fetch()
	var v T
	if err == nil {
		v, err = parse(body)
	}
	if err == nil {
		at := time.Now()
		src := Source{From: FromNetwork, FetchedAt: at}
		src.CacheErr = writeCache(name, cacheFile{FetchedAt: at, Source: source, Body: string(body)})
		return v, src
	}
	if haveCache {
		return fromCache, Source{From: FromCache, FetchedAt: cached.FetchedAt, Err: err}
	}
	return zero, Source{Err: err}
}

func readCache(name string) (cacheFile, bool) {
	b, err := os.ReadFile(cachePath(name))
	if err != nil {
		return cacheFile{}, false
	}
	var c cacheFile
	if json.Unmarshal(b, &c) != nil || c.FetchedAt.IsZero() {
		return cacheFile{}, false
	}
	return c, true
}

func writeCache(name string, c cacheFile) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return writeAtomic(cachePath(name), b)
}

// writeAtomic writes through a temp file in the same directory and renames
// it into place, so a reader never sees half a file and two writers never
// interleave — the later rename simply wins.
func writeAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ---- per-id endpoint lists --------------------------------------------------

// endpointRecord is one id's endpoint providers as fetched. The map key is the
// id exactly as listed, so x:free and x stay distinct entries — they name
// different endpoint sets.
type endpointRecord struct {
	FetchedAt time.Time `json:"fetched_at"`
	Providers []string  `json:"providers"`
}

const endpointCacheName = OpenRouter + "-endpoints"

func readEndpointCache() map[string]endpointRecord {
	m := map[string]endpointRecord{}
	b, err := os.ReadFile(cachePath(endpointCacheName))
	if err != nil {
		return m
	}
	if json.Unmarshal(b, &m) != nil {
		return map[string]endpointRecord{}
	}
	return m
}

// writeEndpointCache merges fresh records over the ones read at the start and
// drops every record past FreshFor: a stale endpoint list is never used (a
// failed fetch is unknown, not old data), so keeping it only grows the file
// as free ids come and go.
func writeEndpointCache(old, fresh map[string]endpointRecord, now time.Time) error {
	out := map[string]endpointRecord{}
	for id, r := range old {
		if isFresh(r.FetchedAt, now) {
			out[id] = r
		}
	}
	for id, r := range fresh {
		out[id] = r
	}
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return writeAtomic(cachePath(endpointCacheName), b)
}
