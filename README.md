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
* The `redis` service connects to any Redis instance and supports read-only clients.
* The `redis-leader` service connects to the current leader.
  Clients are disconnected via [`CLIENT KILL`](https://redis.io/commands/client-kill)
  when the leader changes, to force them to reconnect to the right one.

## Motivation

As with many things, when it comes to distributed systems, the less complexity, the better.

This service aims to be simpler and more reliable than a Redis Sentinel setup in a Kubernetes context.

The Kubernetes control-plane already provides highly-available service discovery
and leader election facilities. Redis Sentinel duplicates a large part, including
algorithms for achieving distributed consensus written in C.

## Attributions

Author: [Richard Patel](https://github.com/terorie)
