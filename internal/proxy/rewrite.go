package proxy

import (
	"bytes"
	"net/url"
	"regexp"
	"strings"

	"go-proxy/internal/config"
)

var (
	cssURLRe    = regexp.MustCompile(`(?i)url\(\s*['"]?([^'"\)\s]+)['"]?\s*\)`)
	cssImportRe = regexp.MustCompile(`(?i)@import\s+['"]([^'"]+)['"]`)
)

// upstreamURL gabungkan target base + path request client.
func upstreamURL(cfg *config.Config, path, rawQuery string) string {
	p := path
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	base := cfg.TargetBase // "" atau "/sub"
	full := strings.TrimSuffix(cfg.Target.Scheme+"://"+cfg.Target.Host+base, "/") + p
	if rawQuery != "" {
		full += "?" + rawQuery
	}
	return full
}

// rewriteLocation: Location dari origin -> path relatif proxy.
func rewriteLocation(cfg *config.Config, loc string) string {
	if loc == "" {
		return loc
	}
	// absolut ke origin
	for _, prefix := range originPrefixes(cfg) {
		if strings.HasPrefix(loc, prefix) {
			rest := strings.TrimPrefix(loc, prefix)
			if rest == "" {
				rest = "/"
			}
			if !strings.HasPrefix(rest, "/") {
				rest = "/" + rest
			}
			return rest
		}
	}
	// //host/...
	if strings.HasPrefix(loc, "//"+cfg.TargetHost) {
		rest := strings.TrimPrefix(loc, "//"+cfg.TargetHost)
		if rest == "" {
			rest = "/"
		}
		return rest
	}
	return loc
}

func originPrefixes(cfg *config.Config) []string {
	base := strings.TrimSuffix(cfg.TargetString, "/")
	host := cfg.TargetHost
	basePath := cfg.TargetBase // "" atau "/sub"
	prefixes := []string{base + "/", base}
	// varian scheme lain + base path, agar redirect http<->https tetap ke-rewrite
	for _, scheme := range []string{"https://", "http://"} {
		withBase := scheme + host + basePath
		prefixes = append(prefixes, withBase+"/", withBase)
		prefixes = append(prefixes, scheme+host+"/", scheme+host)
	}
	return prefixes
}

// rewriteBody dispatch per content-type. Mengembalikan body baru + flag berubah.
func rewriteBody(cfg *config.Config, contentType string, body []byte) ([]byte, bool) {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "text/html"):
		if !cfg.RewriteHTML {
			return body, false
		}
		return rewriteHTML(cfg, body), true
	case strings.Contains(ct, "text/css"):
		if !cfg.RewriteCSS {
			return body, false
		}
		return rewriteCSS(cfg, body), true
	case strings.Contains(ct, "javascript") || strings.Contains(ct, "ecmascript"):
		if !cfg.RewriteJS {
			return body, false
		}
		return rewriteGeneric(cfg, body), true
	default:
		return body, false
	}
}

// rewriteHTML: ganti semua URL absolut origin jadi path relatif proxy.
// Contoh: https://target.com/en/a -> /en/a . Aman untuk src/href/srcset/style/script.
func rewriteHTML(cfg *config.Config, body []byte) []byte {
	return rewriteGeneric(cfg, body)
}

func rewriteCSS(cfg *config.Config, body []byte) []byte {
	out := rewriteGeneric(cfg, body)
	// url(//host/...) sisa -> url(/...)
	out = bytes.ReplaceAll(out, []byte("url(//"+cfg.TargetHost), []byte("url(/"))
	out = bytes.ReplaceAll(out, []byte("url('//"+cfg.TargetHost), []byte("url('/"))
	out = bytes.ReplaceAll(out, []byte(`url("//`+cfg.TargetHost), []byte(`url("/`))
	_ = cssURLRe
	_ = cssImportRe
	return out
}

func rewriteGeneric(cfg *config.Config, body []byte) []byte {
	host := cfg.TargetHost
	basePath := cfg.TargetBase // "" atau "/sub"
	// Urutan penting: paling panjang dulu agar tidak merusak https://... saat replace //host.
	var froms []string
	for _, scheme := range []string{"https://", "http://"} {
		if basePath != "" {
			froms = append(froms, scheme+host+basePath+"/")
		}
		froms = append(froms, scheme+host+"/")
	}
	if basePath != "" {
		froms = append(froms, "//"+host+basePath+"/")
	}
	froms = append(froms, "//"+host+"/")

	out := body
	for _, from := range froms {
		out = bytes.ReplaceAll(out, []byte(from), []byte("/"))
	}
	// Sisa bare host tanpa trailing slash (misal href="https://host") -> "/"
	var bares []string
	for _, scheme := range []string{"https://", "http://"} {
		if basePath != "" {
			bares = append(bares, scheme+host+basePath)
		}
		bares = append(bares, scheme+host)
	}
	if basePath != "" {
		bares = append(bares, "//"+host+basePath)
	}
	bares = append(bares, "//"+host)
	for _, b := range bares {
		out = bytes.ReplaceAll(out, []byte(b), []byte("/"))
	}
	return out
}

// rewriteReferer: Referer/Origin dari sisi proxy (localhost) -> origin, sebelum diteruskan.
func rewriteToUpstream(cfg *config.Config, r *url.URL, proxyHost string) string {
	if r == nil {
		return ""
	}
	s := r.String()
	if proxyHost != "" {
		s = strings.ReplaceAll(s, "http://"+proxyHost, cfg.TargetString)
		s = strings.ReplaceAll(s, "https://"+proxyHost, cfg.TargetString)
	}
	return s
}
