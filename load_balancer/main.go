package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

var logger = slog.Default()

type BackendStatus string

const (
	StatusUp   BackendStatus = "UP"
	StatusDown BackendStatus = "DOWN"
)

type Backend struct {
	url          *url.URL
	status       BackendStatus
	reverseProxy *httputil.ReverseProxy

	mu sync.RWMutex
}

const (
	Retries uint8 = iota
	Attempts
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

type LoadBalancer struct {
	backends []*Backend
	index    uint32
}

func newLoadBalancer(targets []string) *LoadBalancer {
	var lb LoadBalancer

	for _, it := range targets {
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
			if retries < 3 {
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
	next := atomic.AddUint32(&me.index, uint32(1)) % uint32(len(me.backends))
	numIterations := next + uint32(len(me.backends))
	for i := next; i < numIterations; i++ {
		idx := i % uint32(len(me.backends))
		if me.backends[idx].getAliveStatus() == StatusUp {
			if i != next {
				atomic.StoreUint32(&me.index, idx)
			}
			return me.backends[idx]
		}
	}
	return nil
}

func (me *LoadBalancer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	attempts, _ := r.Context().Value(Attempts).(uint8)
	if attempts > 3 {
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
	backend.reverseProxy.ServeHTTP(w, r)
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

// TODO: use cli arguments or config files for targets and port
func main() {
	targets := []string{
		"http://localhost:8080",
		"http://localhost:8081",
		"http://localhost:8082",
	}

	lb := newLoadBalancer(targets)
	server := http.Server{
		Addr:    "localhost:5050",
		Handler: lb,
	}

	logger.Info("starting load balancer", "addr", "localhost:5050")
	if err := server.ListenAndServe(); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}
