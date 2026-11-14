package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Meta struct {
	URL          string              `json:"url"`
	Status       int                 `json:"status"`
	Header       map[string][]string `json:"header"`
	CreatedUnix  int64               `json:"created_unix"`
	TTLSeconds   int64               `json:"ttl_seconds"`
	ContentType  string              `json:"content_type"`
	BodyFileName string              `json:"body_file"`
}

func (m *Meta) Expired() bool {
	if m.TTLSeconds <= 0 {
		return true
	}
	return time.Now().Unix()-m.CreatedUnix > m.TTLSeconds
}

type Cache struct {
	dir      string
	enabled  bool
	maxBytes int64
	mu       sync.Mutex
}

func New(dir string, enabled bool, maxBytes int64) *Cache {
	_ = os.MkdirAll(dir, 0o755)
	c := &Cache{dir: dir, enabled: enabled, maxBytes: maxBytes}
	go c.janitor()
	return c
}

func Key(method, upstreamURL string) string {
	h := sha256.Sum256([]byte(method + "\n" + upstreamURL))
	return hex.EncodeToString(h[:])
}

func (c *Cache) Enabled() bool { return c.enabled }

func (c *Cache) metaPath(key string) string { return filepath.Join(c.dir, key+".meta") }
func (c *Cache) bodyPath(key string) string { return filepath.Join(c.dir, key+".body") }

// Lookup mengembalikan meta + path file body jika HIT dan belum expired.
func (c *Cache) Lookup(key string) (*Meta, string, bool) {
	if !c.enabled {
		return nil, "", false
	}
	mp := c.metaPath(key)
	bp := c.bodyPath(key)
	raw, err := os.ReadFile(mp)
	if err != nil {
		return nil, "", false
	}
	var m Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		_ = os.Remove(mp)
		return nil, "", false
	}
	if m.Expired() {
		_ = os.Remove(mp)
		_ = os.Remove(bp)
		return nil, "", false
	}
	st, err := os.Stat(bp)
	if err != nil || st.Size() == 0 {
		_ = os.Remove(mp)
		return nil, "", false
	}
	m.BodyFileName = bp
	return &m, bp, true
}

// StoreBytes simpan body kecil (hasil rewrite html/css/js).
func (c *Cache) StoreBytes(key, reqURL string, status int, header map[string][]string, contentType string, body []byte, ttl time.Duration) {
	if !c.enabled || status != 200 || len(body) == 0 || ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	bp := c.bodyPath(key)
	mp := c.metaPath(key)
	tmp := bp + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, bp)
	m := Meta{
		URL: reqURL, Status: status, Header: header,
		CreatedUnix: time.Now().Unix(), TTLSeconds: int64(ttl.Seconds()),
		ContentType: contentType,
	}
	raw, _ := json.Marshal(m)
	_ = os.WriteFile(mp, raw, 0o644)
	c.enforceLocked()
}

// CommitTempFile: pindahkan temp file hasil streaming jadi entry cache.
func (c *Cache) CommitTempFile(key, reqURL string, status int, header map[string][]string, contentType string, tmpPath string, ttl time.Duration) {
	if !c.enabled || status != 200 || ttl <= 0 {
		_ = os.Remove(tmpPath)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	bp := c.bodyPath(key)
	mp := c.metaPath(key)
	st, err := os.Stat(tmpPath)
	if err != nil || st.Size() == 0 {
		_ = os.Remove(tmpPath)
		return
	}
	_ = os.Rename(tmpPath, bp)
	m := Meta{
		URL: reqURL, Status: status, Header: header,
		CreatedUnix: time.Now().Unix(), TTLSeconds: int64(ttl.Seconds()),
		ContentType: contentType,
	}
	raw, _ := json.Marshal(m)
	_ = os.WriteFile(mp, raw, 0o644)
	c.enforceLocked()
}

func (c *Cache) TempFile() (*os.File, string, error) {
	return c.tempFile()
}

func (c *Cache) tempFile() (*os.File, string, error) {
	_ = os.MkdirAll(c.dir, 0o755)
	f, err := os.CreateTemp(c.dir, "dl-*.tmp")
	if err != nil {
		return nil, "", err
	}
	return f, f.Name(), nil
}

func (c *Cache) PurgeByURL(upstreamURL string) int {
	// Purge GET entry untuk satu upstream URL.
	n := 0
	for _, m := range []string{"GET"} {
		k := Key(m, upstreamURL)
		if os.Remove(c.metaPath(k)) == nil {
			n++
		}
		_ = os.Remove(c.bodyPath(k))
	}
	return n
}

func (c *Cache) PurgeAll() int {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".meta") || strings.HasSuffix(e.Name(), ".body") {
			if os.Remove(filepath.Join(c.dir, e.Name())) == nil {
				n++
			}
		}
	}
	return n
}

func (c *Cache) janitor() {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for range t.C {
		c.mu.Lock()
		c.cleanupLocked()
		c.enforceLocked()
		c.mu.Unlock()
	}
}

func (c *Cache) cleanupLocked() {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".meta") {
			continue
		}
		key := strings.TrimSuffix(e.Name(), ".meta")
		raw, err := os.ReadFile(filepath.Join(c.dir, e.Name()))
		if err != nil {
			continue
		}
		var m Meta
		if json.Unmarshal(raw, &m) != nil || m.Expired() {
			_ = os.Remove(filepath.Join(c.dir, e.Name()))
			_ = os.Remove(c.bodyPath(key))
		}
	}
}

// enforceLocked: jika total > max, hapus file terlama dulu.
func (c *Cache) enforceLocked() {
	if c.maxBytes <= 0 {
		return
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	type fi struct {
		path string
		size int64
		mod  time.Time
	}
	var files []fi
	var total int64
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".body") {
			continue
		}
		p := filepath.Join(c.dir, e.Name())
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		files = append(files, fi{p, st.Size(), st.ModTime()})
		total += st.Size()
	}
	if total <= c.maxBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= c.maxBytes {
			break
		}
		_ = os.Remove(f.path)
		base := strings.TrimSuffix(filepath.Base(f.path), ".body")
		_ = os.Remove(filepath.Join(c.dir, base+".meta"))
		total -= f.size
	}
}
