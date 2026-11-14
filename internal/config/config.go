package config

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Target       *url.URL
	TargetString string // trimmed, tanpa trailing slash
	TargetHost   string
	TargetBase   string // path base dari TARGET_URL, misal "" atau "/sub"

	Port          string
	CacheDir      string
	CacheEnabled  bool
	MaxSizeBytes  int64
	TTLHTML       time.Duration
	TTLCSS        time.Duration
	TTLJS         time.Duration
	TTLImage      time.Duration
	TTLPDF        time.Duration
	TTLDefault    time.Duration
	RewriteHTML   bool
	RewriteCSS    bool
	RewriteJS     bool
	OriginTimeout time.Duration
	PurgeToken    string
}

func Load() (*Config, error) {
	// Load .env dulu (best effort, env asli tetap menang).
	_ = loadDotEnv(".env")

	targetStr := strings.TrimSpace(getenv("TARGET_URL", ""))
	if targetStr == "" {
		return nil, fmt.Errorf("TARGET_URL kosong, isi di .env")
	}
	u, err := url.Parse(strings.TrimSuffix(targetStr, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("TARGET_URL tidak valid: %s", targetStr)
	}

	port := strings.TrimSpace(getenv("PORT", "8080"))
	if port == "" {
		port = "8080"
	}

	cfg := &Config{
		Target:       u,
		TargetString: strings.TrimSuffix(targetStr, "/"),
		TargetHost:   u.Host,
		TargetBase:   strings.TrimSuffix(u.Path, "/"),

		Port:          port,
		CacheDir:      getenv("CACHE_DIR", "./cache"),
		CacheEnabled:  getenvBool("CACHE_ENABLED", true),
		MaxSizeBytes:  int64(getenvInt("CACHE_MAX_SIZE_MB", 1024)) * 1024 * 1024,
		TTLHTML:       getenvDur("TTL_HTML", 5*time.Minute),
		TTLCSS:        getenvDur("TTL_CSS", time.Hour),
		TTLJS:         getenvDur("TTL_JS", time.Hour),
		TTLImage:      getenvDur("TTL_IMAGE", 24*time.Hour),
		TTLPDF:        getenvDur("TTL_PDF", 7*24*time.Hour),
		TTLDefault:    getenvDur("TTL_DEFAULT", 10*time.Minute),
		RewriteHTML:   getenvBool("REWRITE_HTML", true),
		RewriteCSS:    getenvBool("REWRITE_CSS", true),
		RewriteJS:     getenvBool("REWRITE_JS", false),
		OriginTimeout: getenvDur("ORIGIN_TIMEOUT", 30*time.Second),
		PurgeToken:    strings.TrimSpace(os.Getenv("PURGE_TOKEN")),
	}
	return cfg, nil
}

func (c *Config) TTLForContentType(ct string) time.Duration {
	ct = strings.ToLower(ct)
	switch {
	case strings.Contains(ct, "text/html"):
		return c.TTLHTML
	case strings.Contains(ct, "text/css"):
		return c.TTLCSS
	case strings.Contains(ct, "javascript") || strings.Contains(ct, "ecmascript"):
		return c.TTLJS
	case strings.Contains(ct, "pdf"):
		return c.TTLPDF
	case strings.Contains(ct, "image/"):
		return c.TTLImage
	default:
		return c.TTLDefault
	}
}

// loadDotEnv: parser minimal KEY=VALUE, support quote dan komentar #.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		// env asli menang, jangan overwrite
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
	return sc.Err()
}

func getenv(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func getenvBool(k string, def bool) bool {
	if v, ok := os.LookupEnv(k); ok {
		v = strings.ToLower(strings.TrimSpace(v))
		switch v {
		case "1", "true", "yes", "y", "on":
			return true
		case "0", "false", "no", "n", "off":
			return false
		}
	}
	return def
}

func getenvInt(k string, def int) int {
	if v, ok := os.LookupEnv(k); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func getenvDur(k string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
		if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil {
			return d
		}
	}
	return def
}
