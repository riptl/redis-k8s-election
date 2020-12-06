<div align="center">
  <h1>redis-k8s-election</h1>
  <p>
    <strong>Kubernetes-native leader/follower replication for Redis</strong>
  </p>
  <sub>Built with Go and 👿 at <a href="https://github.com/Blockdaemon">Blockdaemon</a></sub>
</div>

## About

This service implements leader/follower (master/slave) replication for Redis.
It replaces Redis Sentinel using Kubernetes-native leader election.

The list of capabilities is similar to [Sentinel's](https://redis.io/topics/sentinel):
- **Monitoring**. Kubernetes readiness and liveness checks continually check
- **Events**. Events regarding availability checks are handled with K8s-native APIs,
  for example through Alertmanager via [`kube-state-metrics`](https://github.com/kubernetes/kube-state-metrics).
- **Automatic failover**. If a master fails, another is re-elected.

`redis-k8s-election` has certain advantages over Sentinel:
- **Works with any Redis client**, and does not require explicit failover support, nor special configuration.
- **No Sentinel quorum required** to achieve consensus.
  The Redis service stays up as long as it has access to one Redis pod and the Kubernetes control-plane.
- **Resilient against pod rescheduling** by using Pod DNS.
  Redis Sentinel via the [bitnami/redis](https://artifacthub.io/packages/helm/bitnami/redis) Helm chart
  use Pod IPs, which risks running into deadlocks when enough pods reschedule and change IPs.

Reasons to instead use Redis Sentinel or [ledisdb/redis-failover](https://github.com/ledisdb/redis-failover):
- You prefer Redis Sentinel's maturity.
- You don't have an existing Kubernetes environment.
- You don't want to rely on the Kubernetes control plane for availability.
- `k8s.io/client-go/tools/leaderelection`
  - is in alpha state (though it has been stable for ~2 years).
  - does not strictly guarantee that only one Redis instance is leading.
  - has higher latency than Sentinel or etcd.
  - is prone to clock skew larger than the lease duration.

For further information, please check the [Redis Sentinel Documentation](https://redis.io/topics/sentinel).

## Quickstart

The manifests at `examples/cluster.yaml` deploy a three-node Redis leader/replica cluster.

```shell script
kubectl create namespace redis-k8s-election
kubectl apply -n redis-k8s-election -f https://raw.githubusercontent.com/terorie/redis-k8s-election/main/examples/cluster.yaml
```

Connect to the leader for read/write access:

```shell script
kubectl port-forward -n redis-k8s-election svc/redis-leader 6378:6379
redis-cli -p 6378 info replication
```

Or any of the nodes for read access:

```shell script
kubectl port-forward -n redis-k8s-election svc/redis-replica 6379
redis-cli -p 6379 info replication
```

### Election results

`redis-0` gets created first, winning the leader election.
`redis-1` and `redis-2` will follow.

```
$ kubectl logs -n redis-k8s-election redis-0 -c redis-k8s-election
I1206 04:23:06.496554       1 main.go:75] Connecting to Redis: localhost:6379
I1206 04:23:06.525680       1 main.go:91] Successful initial ping from Redis
I1206 04:23:06.525737       1 leaderelection.go:242] attempting to acquire leader lease  redis-k8s-election/redis-leader-lock...
I1206 04:23:06.543659       1 leaderelection.go:252] successfully acquired lease redis-k8s-election/redis-leader-lock
I1206 04:23:06.543864       1 main.go:126] I am the Redis leader
I1206 04:23:06.544041       1 main.go:173] Setting leader service selector to pod redis-0
I1206 04:23:06.554853       1 main.go:151] Setting Redis to replicate NO ONE

$ kubectl logs -n redis-k8s-election redis-1 -c redis-k8s-election
I1206 04:23:17.443600       1 main.go:75] Connecting to Redis: localhost:6379
I1206 04:23:17.468970       1 main.go:91] Successful initial ping from Redis
I1206 04:23:17.475652       1 leaderelection.go:345] lock is held by redis-0 and has not yet expired
I1206 04:23:17.475695       1 main.go:151] Setting Redis to replicate redis-0.redis.redis-k8s-election.svc.cluster.local 6379

$ kubectl logs -n redis-k8s-election redis-2 -c redis-k8s-election
I1206 04:23:33.294719       1 main.go:75] Connecting to Redis: localhost:6379
I1206 04:23:33.320662       1 main.go:91] Successful initial ping from Redis
I1206 04:23:33.328167       1 leaderelection.go:345] lock is held by redis-0 and has not yet expired
I1206 04:23:33.328212       1 main.go:151] Setting Redis to replicate redis-0.redis.redis-k8s-election.svc.cluster.local 6379
```

To test failover, try to terminate `redis-0`.
`redis-1` wins the next election and `redis-2` switches its replica config.

```
$ kubectl logs -n redis-k8s-election redis-1 -c redis-k8s-election
I1206 04:25:17.886144       1 leaderelection.go:252] successfully acquired lease redis-k8s-election/redis-leader-lock
I1206 04:25:17.886282       1 main.go:126] I am the Redis leader
I1206 04:25:17.886439       1 main.go:173] Setting leader service selector to pod redis-1
I1206 04:25:17.908234       1 main.go:151] Setting Redis to replicate NO ONE

$ kubectl logs -n redis-k8s-election redis-2 -c redis-k8s-election
I1206 04:25:19.447584       1 leaderelection.go:345] lock is held by redis-1 and has not yet expired
I1206 04:25:19.447701       1 main.go:151] Setting Redis to replicate redis-1.redis.redis-k8s-election.svc.cluster.local 6379
I1206 04:25:22.121783       1 leaderelection.go:345] lock is held by redis-1 and has not yet expired
```

To clean up your resources when you are done:

```shell script
kubectl delete -n redis-k8s-election -f https://raw.githubusercontent.com/terorie/redis-k8s-election/main/examples/cluster.yaml
kubectl get -n redis-k8s-election pvc -l app=redis,role=node -o name | xargs kubectl delete -n redis-k8s-election
kubectl delete namespace redis-k8s-election
```

## Architecture

TODO: Properly explain this

Coordination

* Each Redis pod runs a `redis-k8s-election` sidecar.
* The sidecars compete in a Kubernetes leader election.
* The leader configures its Redis instance as writable,
  and the followers make themselves read-only replicas of the leader. 

Service discovery

* Clients use Kubernetes Services to connect to Redis.
* All Redis clients are supported, no Sentinel client logic involved.
* The `redis-replica` service connects to any Redis instance and supports read-only clients.
* The `redis-leader` service connects to the current leader.
  The election sidecar proxies all connections to the leader,
  ensuring Redis is only reached when really talking with the leader.

## Motivation

As with many things, when it comes to distributed systems, the less complexity, the better.

This service aims to be simpler and more reliable than a Redis Sentinel setup in a Kubernetes context.

The Kubernetes control-plane already provides highly-available service discovery
and leader election facilities. Redis Sentinel duplicates a large part, including
algorithms for achieving distributed consensus written in C.

## Attributions

Author: [Richard Patel](https://github.com/terorie)

Kubernetes manifests (example/cluster.yaml) based on
[Bitnami Redis Helm chart](https://github.com/bitnami/charts/tree/master/bitnami/redis).
