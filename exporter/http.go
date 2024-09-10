package exporter

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"net/http"
	"net/url"
	"strings"
)

func (e *Exporter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.mux.ServeHTTP(w, r)
}

func (e *Exporter) healthHandler(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte(`ok`))
}

func (e *Exporter) indexHandler(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte(`<html>
<head><title>Redis Exporter ` + e.buildInfo.Version + `</title></head>
<body>
<h1>Redis Exporter ` + e.buildInfo.Version + `</h1>
<p><a href='` + e.options.MetricsPath + `'>Metrics</a></p>
</body>
</html>
`))
}

func (e *Exporter) metricHandler(w http.ResponseWriter, r *http.Request) {
	promhttp.HandlerFor(
		e.options.Registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError},
	).ServeHTTP(w, r)
}

func (e *Exporter) scrapeHandler(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if target == "" {
		http.Error(w, "'target' parameter must be specified", http.StatusBadRequest)
		e.targetScrapeRequestErrors.Inc()
		return
	}

	if !strings.Contains(target, "://") {
		target = "redis://" + target
	}

	u, err := url.Parse(target)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid 'target' parameter, parse err: %ck ", err), http.StatusBadRequest)
		e.targetScrapeRequestErrors.Inc()
		return
	}

	// get rid of username/password info in "target" so users don't send them in plain text via http
	u.User = nil
	target = u.String()

	opts := e.options

	if ck := r.URL.Query().Get("check-keys"); ck != "" {
		opts.CheckKeys = ck
	}

	if csk := r.URL.Query().Get("check-single-keys"); csk != "" {
		opts.CheckSingleKeys = csk
	}

	if cs := r.URL.Query().Get("check-streams"); cs != "" {
		opts.CheckStreams = cs
	}

	if css := r.URL.Query().Get("check-single-streams"); css != "" {
		opts.CheckSingleStreams = css
	}

	if cntk := r.URL.Query().Get("count-keys"); cntk != "" {
		opts.CountKeys = cntk
	}

	registry := prometheus.NewRegistry()
	opts.Registry = registry

	_, err = NewRedisExporter(target, opts)
	if err != nil {
		http.Error(w, "NewRedisExporter() err: err", http.StatusBadRequest)
		e.targetScrapeRequestErrors.Inc()
		return
	}

	promhttp.HandlerFor(
		registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError},
	).ServeHTTP(w, r)
}

func (e *Exporter) reloadPwdFile(w http.ResponseWriter, r *http.Request) {
	if e.options.RedisPwdFile == "" {
		http.Error(w, "There is no pwd file specified", http.StatusBadRequest)
		return
	}
	log.Debugf("Reload redisPwdFile")
	passwordMap, err := LoadPwdFile(e.options.RedisPwdFile)
	if err != nil {
		log.Errorf("Error reloading redis passwords from file %s, err: %s", e.options.RedisPwdFile, err)
		http.Error(w, "failed to reload passwords file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	e.Lock()
	e.options.PasswordMap = passwordMap
	e.Unlock()
	_, _ = w.Write([]byte(`ok`))
}

/*
basic auth handler
*/
func (e *Exporter) basicAuth(next http.HandlerFunc) http.HandlerFunc {
	exporterUser := e.options.BasicAuth.ExporterUser
	exporterPwd := e.options.BasicAuth.ExporterPwd
	return func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if ok {
			// Calculate SHA-256 hashes for the provided and expected usernames and passwords.
			usernameHash := sha256.Sum256([]byte(username))
			passwordHash := sha256.Sum256([]byte(password))
			expectedUsernameHash := sha256.Sum256([]byte(exporterUser))
			expectedPasswordHash := sha256.Sum256([]byte(exporterPwd))

			usernameMatch := subtle.ConstantTimeCompare(usernameHash[:], expectedUsernameHash[:]) == 1
			passwordMatch := subtle.ConstantTimeCompare(passwordHash[:], expectedPasswordHash[:]) == 1

			if usernameMatch && passwordMatch {
				next.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="restricted", charset="UTF-8"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}
}
