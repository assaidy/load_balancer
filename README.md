### Simple Load Balancer in Go
The load balancing algorithm used is **dynamically-weighted round robin**.

### References
- Load Balancing: https://samwho.dev/load-balancing/
- Let's Create a Simple Load Balancer With Go: https://kasvith.me/posts/lets-create-a-simple-lb-go/

### Usage
start some backend servers:
```bash
go run backend_server/main.go -port 8080
go run backend_server/main.go -port 8081
go run backend_server/main.go -port 8082
```

now start load balancer and register backends:
```bash
go run load_balancer/main.go -port 5050 \
    -add-backend http://localhost:8080 \
    -add-backend http://localhost:8081 \
    -add-backend http://localhost:8082 
```

*run both commands with `-help` for more info.*
