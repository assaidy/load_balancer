package main

import (
	"cmp"
	"context"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"slices"
	"sync"
	"time"
)

var logger = slog.Default()

type BackendStatus string

const (
	StatusUp   BackendStatus = "UP"
	StatusDown BackendStatus = "DOWN"
)

type Backend struct {
	url            *url.URL
	status         BackendStatus
	reverseProxy   *httputil.ReverseProxy
	numConnections uint32

	mu sync.RWMutex
}

const (
	Retries     = "Retries"
	MaxRetries  = 3
	Attempts    = "Attempts"
	MaxAttempts = 3
)

func (me *Backend) checkHealth() {
	conn, err := net.DialTimeout("tcp", me.url.Host, 1*time.Second)
	if err != nil {
		logger.Warn("backend health check failed", "url", me.url.String(), "error", err)
		me.setStatus(StatusDown)
		return
	}
	conn.Close()
	me.setStatus(StatusUp)
	logger.Info("backend health check succeeded", "url", me.url.String())
}

func (me *Backend) setStatus(status BackendStatus) {
	me.mu.Lock()
	defer me.mu.Unlock()
	oldStatus := me.status
	me.status = status
	if oldStatus != status {
		logger.Info("backend status changed", "url", me.url.String(), "status", status)
	}
}

func (me *Backend) getAliveStatus() BackendStatus {
	me.mu.RLock()
	defer me.mu.RUnlock()
	return me.status
}

func (me *Backend) incrementNumConnections() {
	me.mu.RLock()
	me.numConnections += 1
	me.mu.RUnlock()
}

func (me *Backend) decrementNumConnections() {
	me.mu.RLock()
	me.numConnections -= 1
	me.mu.RUnlock()
}

type LoadBalancer struct {
	backends []*Backend
}

func newLoadBalancer(backendURLs []string) *LoadBalancer {
	var lb LoadBalancer

	for _, it := range backendURLs {
		url, err := url.Parse(it)
		if err != nil {
			logger.Error("failed to parse backend URL", "url", it, "error", err)
			os.Exit(1)
		}
		backend := Backend{
			url:    url,
			status: StatusUp,
		}
		proxy := httputil.NewSingleHostReverseProxy(backend.url)
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			retries, _ := r.Context().Value(Retries).(uint8)
			if retries < MaxRetries {
				logger.Info("retrying request", "url", r.URL.String(), "retry", retries+1)
				// give some time for the backend to recover if it has any errors
				time.Sleep(10 * time.Millisecond)
				ctx := context.WithValue(r.Context(), Retries, retries+1)
				proxy.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			// after 3 retries mark backend as down
			logger.Warn("marking backend as down after retries failed", "url", backend.url.String(), "error", err)
			backend.setStatus(StatusDown)
			// count attempts to handle the request with different backend
			attempts, _ := r.Context().Value(Attempts).(uint8)
			ctx := context.WithValue(r.Context(), Attempts, attempts+1)
			lb.ServeHTTP(w, r.WithContext(ctx))
		}
		backend.reverseProxy = proxy
		lb.backends = append(lb.backends, &backend)
		logger.Info("registered backend", "url", it)
	}

	go lb.startHealthCheck()
	return &lb
}

func (me *LoadBalancer) getNextBackend() *Backend {
	if len(me.backends) == 0 {
		return nil
	}

	// TODO: use a min heap O(1) instead of sorting each time O(n)
	slices.SortFunc(me.backends, func(a, b *Backend) int {
		return cmp.Compare(a.numConnections, b.numConnections)
	})
	for _, it := range me.backends {
		for it.getAliveStatus() == StatusUp {
			return it
		}
	}

	return nil
}

func (me *LoadBalancer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	attempts, _ := r.Context().Value(Attempts).(uint8)
	if attempts > MaxAttempts {
		logger.Error("max attempts reached", "path", r.URL.Path, "method", r.Method)
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}

	backend := me.getNextBackend()
	if backend == nil {
		logger.Error("no healthy backends available", "path", r.URL.Path, "method", r.Method)
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	logger.Info("forwarding request", "path", r.URL.Path, "method", r.Method, "backend", backend.url.String())
	backend.incrementNumConnections()
	backend.reverseProxy.ServeHTTP(w, r)
	backend.decrementNumConnections()
}

func (me *LoadBalancer) startHealthCheck() {
	ticker := time.NewTicker(20 * time.Second)
	logger.Info("starting health check routine", "interval", "20s")
	for range ticker.C {
		logger.Info("running health checks on all backends")
		for _, it := range me.backends {
			it.checkHealth()
		}
	}
}

func main() {
	var port string
	var backendURLs []string
	flag.StringVar(&port, "port", "5050", "port to listen at")
	flag.Func("add-backend", "Add a backend target URL (can be used multiple times)", func(s string) error {
		backendURLs = append(backendURLs, s)
		return nil
	})
	flag.Parse()

	lb := newLoadBalancer(backendURLs)
	addr := "localhost:" + port
	server := http.Server{
		Addr:    addr,
		Handler: lb,
	}

	logger.Info("starting load balancer", "addr", addr)
	if err := server.ListenAndServe(); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}
