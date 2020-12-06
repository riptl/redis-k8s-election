module github.com/Blockdaemon/redis-k8s-election

go 1.15

require (
	github.com/go-redis/redis/v8 v8.4.0
	github.com/google/tcpproxy v0.0.0-20200125044825-b6bb9b5b8252
	k8s.io/apimachinery v0.18.12
	k8s.io/client-go v0.18.12
	k8s.io/klog v1.0.0
)
