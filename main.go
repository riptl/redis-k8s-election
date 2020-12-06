package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/tcpproxy"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog"
)

var requiredFlags = map[string]struct{}{
	"leader-service":   {},
	"lock":             {},
	"headless-service": {},
}

func main() {
	leaderSvcName := flag.String("leader-service", "", "Name of the Service exposing the leader")
	lockName := flag.String("lock", "", "Name of the Kubernetes lease lock")
	redisPort := flag.Uint("redis-port", 6379, "Port exposed by Redis server")
	leaderPort := flag.Uint("leader-port", 6378, "Port exposed by this sidecar for accepting leader connections")
	clusterDomain := flag.String("cluster-domain", "cluster.local", "Kubernetes cluster domain")
	headlessSvcName := flag.String("headless-service", "", "Name of the headless service attached to the StatefulSet")
	klog.InitFlags(flag.CommandLine)
	flag.Parse()
	flag.Visit(func(f *flag.Flag) {
		delete(requiredFlags, f.Name)
	})

	hostname, err := os.Hostname()
	if err != nil {
		klog.Fatal("Unable to determine hostname: ", err)
	}
	for n := range requiredFlags {
		klog.Fatal("Missing required flag -", n)
	}
	if *redisPort <= 0 || *redisPort > 0xFFFF {
		klog.Fatal("Invalid value for -redis-port")
	}
	redisPortStr := strconv.FormatUint(uint64(*redisPort), 10)
	if *leaderPort <= 0 || *leaderPort > 0xFFFF {
		klog.Fatal("Invalid value for -leader-port")
	}
	leaderListen := ":" + strconv.FormatUint(uint64(*leaderPort), 10)

	// Build context that cancels with SIGINT.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		c := make(chan os.Signal)
		signal.Notify(c, syscall.SIGTERM)
		<-c
		cancel()
	}()

	// Connect to Kubernetes in-cluster API.
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		klog.Fatal("Failed to build in-cluster-config: ", err)
	}
	k8s := kubernetes.NewForConfigOrDie(k8sConfig)
	namespace := getKubernetesNamespace()

	// Build Redis client and try to get a successful ping.
	klog.V(2).Info("Connecting to Redis: localhost:", *redisPort)
	redisClient := redis.NewClient(&redis.Options{
		Network:         "tcp",
		Addr:            "localhost:" + redisPortStr,
		DB:              0,
		MaxRetries:      20,
		MinRetryBackoff: 8 * time.Millisecond,
		MaxRetryBackoff: 512 * time.Millisecond,
		DialTimeout:     5 * time.Second,
		ReadTimeout:     3 * time.Second,
		WriteTimeout:    3 * time.Second,
	})
	defer redisClient.Close()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		klog.Fatal("Failed to get initial ping from Redis: ", err)
	}
	klog.V(2).Info("Successful initial ping from Redis")

	// Initialize TCP proxy for leader.
	var proxy tcpproxy.Proxy
	proxy.AddRoute(leaderListen, tcpproxy.To("localhost:6379"))
	var proxyLock sync.Mutex

	// Start Kubernetes leader election.
	lec := leaderelection.LeaderElectionConfig{
		Name: "redis",
		Lock: &resourcelock.LeaseLock{
			LeaseMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      *lockName,
			},
			Client: k8s.CoordinationV1(),
			LockConfig: resourcelock.ResourceLockConfig{
				Identity: hostname,
			},
		},
		LeaseDuration: 15 * time.Second,
		RenewDeadline: 10 * time.Second,
		RetryPeriod:   2 * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				services := k8s.CoreV1().Services(namespace)
				if err := updateLeaderService(ctx, services, *leaderSvcName, hostname); err != nil {
					klog.Error("Failed to update Kubernetes leader service: ", err)
					cancel()
					return
				}
				if err := setReplicaOf(ctx, redisClient, "NO", "ONE"); err != nil {
					klog.Error("Failed to remove replication config from Redis server: ", err)
					cancel()
					return
				}
				proxyLock.Lock()
				proxyErr := proxy.Start()
				proxyLock.Unlock()
				if proxyErr != nil {
					klog.Error("Failed to start leader TCP proxy: ", proxyErr)
					cancel()
					return
				}
				go func() {
					if err := proxy.Wait(); err != nil {
						klog.Fatal("Leader TCP proxy failed: ", err)
					}
				}()
				klog.V(2).Info("Started leader TCP proxy at ", leaderListen)
			},
			OnStoppedLeading: func() {
				proxyLock.Lock()
				proxyErr := proxy.Close()
				proxyLock.Unlock()
				if proxyErr != nil {
					klog.Fatal("Failed to stop leader TCP proxy: ", proxyErr)
				}
				klog.V(2).Info("Stopped leader TCP proxy")
			},
			OnNewLeader: func(identity string) {
				if identity == hostname {
					klog.V(2).Info("I am the Redis leader")
					return
				}
				podDNS := fmt.Sprintf("%s.%s.%s.svc.%s", identity, *headlessSvcName, namespace, *clusterDomain)
				if err := setReplicaOf(ctx, redisClient, podDNS, redisPortStr); err != nil {
					klog.Fatal("Failed to replicate new leader: ", err)
				}
			},
		},
		ReleaseOnCancel: false,
	}
	leaderelection.RunOrDie(ctx, lec)
}

func getKubernetesNamespace() string {
	namespace, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		klog.Fatal("Failed to read namespace: ", err)
	}
	return strings.TrimSpace(string(namespace))
}

// setReplicaOf reconfigures the Redis instance against another.
// Ported from https://fossies.org/linux/redis/src/sentinel.c sentinelSendSlaveOf()
func setReplicaOf(ctx context.Context, rd *redis.Client, host, port string) error {
	klog.V(2).Infof("Setting Redis to replicate %s %s", host, port)
	_, err := rd.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		if err := pipe.SlaveOf(ctx, host, port).Err(); err != nil {
			return err
		}
		// TODO Should config be persisted
		//if err := pipe.ConfigRewrite(ctx).Err(); err != nil {
		//	return err
		//}
		if err := pipe.ClientKillByFilter(ctx, "TYPE", "normal").Err(); err != nil {
			return err
		}
		if err := pipe.ClientKillByFilter(ctx, "TYPE", "pubsub").Err(); err != nil {
			return err
		}
		return nil
	})
	return err
}

// updateLeaderService points the leader Kubernetes service to the current Redis leader pod.
func updateLeaderService(ctx context.Context, services corev1.ServiceInterface, svc, pod string) (err error) {
	klog.V(2).Info("Setting leader service selector to pod ", pod)
	patches := []jsonPatchOp{
		{
			Op:   "replace",
			Path: "/spec/selector",
			Value: map[string]string{
				"statefulset.kubernetes.io/pod-name": pod,
			},
		},
	}
	patchesJSON, err := json.Marshal(patches)
	if err != nil {
		panic(err)
	}
	_, err = services.Patch(ctx, svc, types.JSONPatchType, patchesJSON, metav1.PatchOptions{})
	return
}

// jsonPatchOp is an RFC 6902 JSON Patch operation.
type jsonPatchOp struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value"`
}
