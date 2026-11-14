package proxy

import (
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go-proxy/internal/config"
)

func copyRequestHeaders(src *http.Request, dst *http.Request, cfg *config.Config) {
	for k, vv := range src.Header {
		ck := http.CanonicalHeaderKey(k)
		if hopHeaders[ck] {
			continue
		}
		if ck == "Accept-Encoding" {
			continue // paksa identity agar rewrite/cache konsisten
		}
		for _, v := range vv {
			dst.Header.Add(ck, v)
		}
	}
	// Host = origin
	dst.Host = cfg.Target.Host
	dst.Header.Set("X-Forwarded-Host", src.Host)
	if src.TLS != nil {
		dst.Header.Set("X-Forwarded-Proto", "https")
	} else {
		if p := src.Header.Get("X-Forwarded-Proto"); p != "" {
			dst.Header.Set("X-Forwarded-Proto", p)
		} else {
			dst.Header.Set("X-Forwarded-Proto", "http")
		}
	}
	if ip := src.Header.Get("X-Forwarded-For"); ip != "" {
		dst.Header.Set("X-Forwarded-For", ip+", "+src.RemoteAddr)
	} else {
		dst.Header.Set("X-Forwarded-For", src.RemoteAddr)
	}
	// Referer/Origin yang menunjuk ke proxy -> kembalikan ke origin
	if ref := src.Header.Get("Referer"); ref != "" {
		if strings.Contains(ref, src.Host) {
			dst.Header.Set("Referer", rewriteToUpstream(cfg, mustParse(ref), src.Host))
		}
	}
	if org := src.Header.Get("Origin"); org != "" {
		if strings.Contains(org, src.Host) {
			dst.Header.Set("Origin", cfg.Target.Scheme+"://"+cfg.Target.Host)
		}
	}
	if ua := src.Header.Get("User-Agent"); ua == "" {
		dst.Header.Set("User-Agent", "go-proxy/1.0")
	}
	dst.Header.Set("Accept-Encoding", "identity")
}

func cloneHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vv := range h {
		cp := make([]string, len(vv))
		copy(cp, vv)
		out[k] = cp
	}
	return out
}

func headerToMap(h http.Header) map[string][]string {
	m := make(map[string][]string, len(h))
	for k, vv := range h {
		if hopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		cp := make([]string, len(vv))
		copy(cp, vv)
		m[k] = cp
	}
	// jangan simpan header volatile
	delete(m, "Date")
	return m
}

func stripCookieDomain(setCookie string) string {
	parts := strings.Split(setCookie, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if strings.HasPrefix(strings.ToLower(t), "domain=") {
			continue
		}
		out = append(out, t)
	}
	return strings.Join(out, "; ")
}

func isRewriteable(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "text/html") ||
		strings.Contains(ct, "text/css") ||
		strings.Contains(ct, "javascript") ||
		strings.Contains(ct, "ecmascript")
}

func writeHeaders(w http.ResponseWriter, h http.Header, status int, cacheStatus string) {
	for k, vv := range h {
		if hopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("X-Cache", cacheStatus)
}

func logLine(r *http.Request, status int, bytes int64, dur time.Duration, cache string) {
	log.Printf("%s %s -> %d %dB %s [%s]", r.Method, r.URL.RequestURI(), status, bytes, dur.Round(time.Millisecond), cache)
}

func mustParse(s string) *url.URL {
	u, _ := url.Parse(s)
	return u
}
