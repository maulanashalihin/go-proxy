package proxy

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"go-proxy/internal/cache"
	"go-proxy/internal/config"
)

const maxRewriteBuffer = 15 << 20 // 15MB, di atas ini selalu streaming tanpa rewrite

var hopHeaders = map[string]bool{
	"Connection": true, "Proxy-Connection": true, "Keep-Alive": true,
	"Proxy-Authenticate": true, "Proxy-Authorization": true,
	"Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

type Handler struct {
	cfg    *config.Config
	cache  *cache.Cache
	client *http.Client
}

func New(cfg *config.Config, c *cache.Cache) *Handler {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		DisableCompression:    true, // minta identity agar bisa rewrite + cache konsisten
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	return &Handler{
		cfg:   cfg,
		cache: c,
		client: &http.Client{
			Transport: tr,
			Timeout:   cfg.OriginTimeout,
			// jangan follow redirect otomatis, biar Location bisa di-rewrite
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/__health", h.health)
	mux.HandleFunc("/__purge", h.purge)
	mux.HandleFunc("/__purge-all", h.purgeAll)
	mux.HandleFunc("/", h.serveProxy)
	return mux
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok": true, "target": h.cfg.TargetString, "port": h.cfg.Port,
		"cache": h.cache.Enabled(), "time": time.Now().UTC(),
	})
}

func (h *Handler) purge(w http.ResponseWriter, r *http.Request) {
	if !h.checkPurgeToken(w, r) {
		return
	}
	q := r.URL.Query().Get("url")
	if q == "" {
		q = r.URL.Query().Get("path")
	}
	if q == "" {
		http.Error(w, "pakai ?url=/path-atau-full-url", 400)
		return
	}
	up := h.resolveUpstreamFromInput(q)
	n := h.cache.PurgeByURL(up)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "purged": n, "upstream": up})
}

func (h *Handler) purgeAll(w http.ResponseWriter, r *http.Request) {
	if !h.checkPurgeToken(w, r) {
		return
	}
	n := h.cache.PurgeAll()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "files_removed": n})
}

func (h *Handler) checkPurgeToken(w http.ResponseWriter, r *http.Request) bool {
	if h.cfg.PurgeToken == "" {
		return true
	}
	got := r.URL.Query().Get("token")
	if got == "" {
		got = r.Header.Get("X-Purge-Token")
	}
	if got != h.cfg.PurgeToken {
		http.Error(w, "purge token salah", 403)
		return false
	}
	return true
}

func (h *Handler) resolveUpstreamFromInput(q string) string {
	q = strings.TrimSpace(q)
	if strings.HasPrefix(q, "http://") || strings.HasPrefix(q, "https://") {
		return q
	}
	if !strings.HasPrefix(q, "/") {
		q = "/" + q
	}
	base := strings.TrimSuffix(h.cfg.TargetString, "/")
	return base + q
}

func (h *Handler) serveProxy(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	upstream := upstreamURL(h.cfg, r.URL.Path, r.URL.RawQuery)
	cacheKey := cache.Key("GET", upstream)

	// Hanya GET yang di-cache. Non-GET langsung proxy tanpa cache.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.proxyPass(w, r, upstream, "BYPASS", start)
		return
	}
	// Range request (PDF viewer) jangan di-cache, streaming langsung.
	if r.Header.Get("Range") != "" || r.Header.Get("If-Range") != "" {
		h.proxyPass(w, r, upstream, "BYPASS-RANGE", start)
		return
	}

	// Coba cache dulu (GET saja, HEAD tidak ambil body cache).
	if r.Method == http.MethodGet {
		if meta, bodyPath, ok := h.cache.Lookup(cacheKey); ok {
			h.serveFromCache(w, r, meta, bodyPath, start)
			return
		}
	}

	h.fetchAndServe(w, r, upstream, cacheKey, start)
}

func (h *Handler) serveFromCache(w http.ResponseWriter, r *http.Request, meta *cache.Meta, bodyPath string, start time.Time) {
	for k, vv := range meta.Header {
		if hopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("X-Cache", "HIT")
	w.Header().Set("X-Proxy-Target", h.cfg.TargetString)
	f, err := os.Open(bodyPath)
	if err != nil {
		http.Error(w, "cache rusak", 500)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	http.ServeContent(w, r, "", st.ModTime(), f)
	logLine(r, meta.Status, st.Size(), time.Since(start), "HIT")
}

func (h *Handler) fetchAndServe(w http.ResponseWriter, r *http.Request, upstream, cacheKey string, start time.Time) {
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstream, nil)
	if err != nil {
		http.Error(w, "upstream url invalid", 500)
		return
	}
	copyRequestHeaders(r, req, h.cfg)

	resp, err := h.client.Do(req)
	if err != nil {
		http.Error(w, "origin unreachable: "+err.Error(), 502)
		return
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	cacheable := h.isCacheable(r, resp)

	// Siapkan header respons untuk client (rewrite Location + bersihkan hop headers).
	outHeader := cloneHeader(resp.Header)
	outHeader.Del("Content-Length")
	outHeader.Del("Content-Encoding")
	outHeader.Del("Transfer-Encoding")
	outHeader.Del("Connection")
	if loc := outHeader.Get("Location"); loc != "" {
		outHeader.Set("Location", rewriteLocation(h.cfg, loc))
	}
	// Set-Cookie: hilangkan Domain origin agar cookie nempel di proxy.
	cookies := outHeader["Set-Cookie"]
	if len(cookies) > 0 {
		fixed := make([]string, 0, len(cookies))
		for _, c := range cookies {
			fixed = append(fixed, stripCookieDomain(c))
		}
		outHeader["Set-Cookie"] = fixed
	}

	isText := isRewriteable(contentType)
	shouldBuffer := isText && (h.cfg.RewriteHTML || h.cfg.RewriteCSS || h.cfg.RewriteJS)

	if shouldBuffer && resp.StatusCode == http.StatusOK {
		// Buffer terbatas untuk rewrite.
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxRewriteBuffer+1))
		if err != nil {
			http.Error(w, "gagal baca origin", 502)
			return
		}
		if len(body) > maxRewriteBuffer {
			// Terlalu besar, fallback streaming tanpa cache/rewrite.
			writeHeaders(w, outHeader, resp.StatusCode, "MISS-BIG")
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(body)
			_, _ = io.Copy(w, resp.Body)
			logLine(r, resp.StatusCode, int64(len(body)), time.Since(start), "MISS-BIG")
			return
		}
		newBody, _ := rewriteBody(h.cfg, contentType, body)
		// simpan ke cache
		if cacheable {
			ttl := h.cfg.TTLForContentType(contentType)
			h.cache.StoreBytes(cacheKey, upstream, resp.StatusCode, headerToMap(outHeader), contentType, newBody, ttl)
		}
		writeHeaders(w, outHeader, resp.StatusCode, "MISS")
		w.Header().Set("X-Proxy-Target", h.cfg.TargetString)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(newBody)
		status := "MISS"
		if !cacheable {
			status = "BYPASS"
		}
		logLine(r, resp.StatusCode, int64(len(newBody)), time.Since(start), status)
		return
	}

	// Binary / besar: streaming + tee ke file cache (jika cacheable).
	if cacheable && resp.StatusCode == http.StatusOK && r.Method == http.MethodGet {
		tmp, tmpPath, err := h.cache.TempFile()
		if err != nil {
			h.streamDirect(w, resp, outHeader, start)
			return
		}
		writeHeaders(w, outHeader, resp.StatusCode, "MISS")
		w.WriteHeader(resp.StatusCode)
		mw := io.MultiWriter(w, tmp)
		n, copyErr := io.Copy(mw, resp.Body)
		_ = tmp.Close()
		if copyErr == nil && n > 0 {
			ttl := h.cfg.TTLForContentType(contentType)
			h.cache.CommitTempFile(cacheKey, upstream, resp.StatusCode, headerToMap(outHeader), contentType, tmpPath, ttl)
		} else {
			_ = os.Remove(tmpPath)
		}
		logLine(r, resp.StatusCode, n, time.Since(start), "MISS")
		return
	}

	// Non-cacheable: streaming langsung.
	h.streamDirectWithHeader(w, resp, outHeader, start)
}

func (h *Handler) streamDirect(w http.ResponseWriter, resp *http.Response, outHeader http.Header, start time.Time) {
	h.streamDirectWithHeader(w, resp, outHeader, start)
}

func (h *Handler) streamDirectWithHeader(w http.ResponseWriter, resp *http.Response, outHeader http.Header, start time.Time) {
	writeHeaders(w, outHeader, resp.StatusCode, "BYPASS")
	w.WriteHeader(resp.StatusCode)
	// butuh r untuk log? pakai resp.Request
	n, _ := io.Copy(w, resp.Body)
	var method, path string
	if resp.Request != nil {
		method, path = resp.Request.Method, resp.Request.URL.Path
	}
	_ = method
	_ = path
	_ = n
	_ = start
}

func (h *Handler) proxyPass(w http.ResponseWriter, r *http.Request, upstream, cacheStatus string, start time.Time) {
	var body io.Reader
	if r.Body != nil {
		body = r.Body
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstream, body)
	if err != nil {
		http.Error(w, "upstream url invalid", 500)
		return
	}
	copyRequestHeaders(r, req, h.cfg)
	resp, err := h.client.Do(req)
	if err != nil {
		http.Error(w, "origin unreachable: "+err.Error(), 502)
		return
	}
	defer resp.Body.Close()
	outHeader := cloneHeader(resp.Header)
	outHeader.Del("Content-Length")
	outHeader.Del("Content-Encoding")
	outHeader.Del("Transfer-Encoding")
	outHeader.Del("Connection")
	if loc := outHeader.Get("Location"); loc != "" {
		outHeader.Set("Location", rewriteLocation(h.cfg, loc))
	}
	writeHeaders(w, outHeader, resp.StatusCode, cacheStatus)
	w.WriteHeader(resp.StatusCode)
	n, _ := io.Copy(w, resp.Body)
	logLine(r, resp.StatusCode, n, time.Since(start), cacheStatus)
}

func (h *Handler) isCacheable(r *http.Request, resp *http.Response) bool {
	if !h.cache.Enabled() {
		return false
	}
	if r.Method != http.MethodGet {
		return false
	}
	if resp.StatusCode != http.StatusOK {
		return false
	}
	if r.Header.Get("Authorization") != "" {
		return false
	}
	cc := strings.ToLower(resp.Header.Get("Cache-Control"))
	if strings.Contains(cc, "no-store") {
		return false
	}
	// Jangan cache HTML yang set session (login) agar tidak bocor antar user.
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "text/html") && len(resp.Header["Set-Cookie"]) > 0 {
		return false
	}
	return true
}
