package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

type Backend struct {
	url          *url.URL
	isAlive      bool
	reverseProxy *httputil.ReverseProxy

	mu sync.RWMutex
}

const (
	Retries uint8 = iota
	Attempts
)

func (me *Backend) checkHealth() {
	me.mu.Lock()
	defer me.mu.Unlock()

	conn, err := net.DialTimeout("tcp", me.url.Host, 2*time.Second)
	if err != nil {
		me.isAlive = false
		return
	}
	conn.Close()
	me.isAlive = true
}

func (me *Backend) setAliveStatus(isAlive bool) {
	me.mu.Lock()
	defer me.mu.Unlock()
	me.isAlive = isAlive
}

func (me *Backend) getAliveStatus() bool {
	me.mu.RLock()
	defer me.mu.RUnlock()
	return me.isAlive
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
			log.Fatal(err)
		}
		backend := Backend{
			url:     url,
			isAlive: true,
		}
		proxy := httputil.NewSingleHostReverseProxy(backend.url)
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			retries, _ := r.Context().Value(Retries).(uint8)
			if retries < 3 {
				time.Sleep(10 * time.Millisecond)
				ctx := context.WithValue(r.Context(), Retries, retries+1)
				proxy.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			// after 3 retries mark backend as down
			backend.setAliveStatus(false)
			// count attempts to handle the request with different backend
			attempts, _ := r.Context().Value(Attempts).(uint8)
			ctx := context.WithValue(r.Context(), Attempts, attempts+1)
			lb.ServeHTTP(w, r.WithContext(ctx))
		}
		backend.reverseProxy = proxy
		lb.backends = append(lb.backends, &backend)
	}

	go lb.startHealthCheck()
	return &lb
}

func (me *LoadBalancer) getNextBackend() *Backend {
	next := atomic.AddUint32(&me.index, uint32(1)) % uint32(len(me.backends))
	numIterations := next + uint32(len(me.backends))
	for i := next; i < uint32(numIterations); i++ {
		idx := i % uint32(len(me.backends))
		if me.backends[idx].getAliveStatus() {
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
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}

	backend := me.getNextBackend()
	if backend == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	backend.reverseProxy.ServeHTTP(w, r)
}

func (me *LoadBalancer) startHealthCheck() {
	ticker := time.NewTicker(10 * time.Second)
	for range ticker.C {
		for _, it := range me.backends {
			it.checkHealth()
		}
	}
}

// TODO:
// [] add propper logging
// [] Implement dynamically weighted round-robin/least connections
// [] Use a heap for sort out alive backends to reduce search surface
// [] Add support for a configuration file/cli interface
// [] Collect statistics

func main() {
	targets := []string{
		"localhost:8080",
		"localhost:8081",
		"localhost:8082",
	}
	lb := newLoadBalancer(targets)
	server := http.Server{
		Addr:    "localhost:5050",
		Handler: lb,
	}
	log.Println("starting load balancer at localhost:5050...")
	log.Fatal(server.ListenAndServe())
}
