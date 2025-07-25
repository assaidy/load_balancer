package main

import (
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"time"
)

func main() {
	var port string
	flag.StringVar(&port, "port", "8080", "port to listen at")
	flag.Parse()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		slog.Info("new request", "addr", r.RemoteAddr)
		w.Write([]byte("hello from backend server\n"))
	})

	slog.Info(fmt.Sprintf("listening at localhost:%s", port))
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
