# Grafana dashboard — Weft Network

`weft-network.json` is a Grafana 10.x dashboard covering the metric
catalog documented in [`../../OBSERVABILITY.md`](../../OBSERVABILITY.md).
It surfaces build fingerprint, etcd connectivity, RPC rate / error
rate / p50 + p99 latency, and replica reachability.

## Import into Grafana

Two equivalent paths, both via the Grafana UI :

1. **File upload** : `Dashboards → New → Import → Upload JSON file`
   and pick `deploy/grafana/weft-network.json`.
2. **Paste JSON** : `Dashboards → New → Import` and paste the file
   contents into the JSON textarea, then `Load`.

On the next screen Grafana asks for the **Prometheus** datasource
(`${DS_PROMETHEUS}`) — pick whichever datasource scrapes the
`weft-network` job. The mapping is per-import ; switching datasource
later means re-importing or editing the dashboard JSON model.

## Prometheus scrape config

The daemon serves metrics on `--metrics-addr` (default `:9100`) on a
listener separate from the gRPC control plane. Add it to your
`prometheus.yml` :

```yaml
scrape_configs:
  - job_name: weft-network
    static_configs:
      - targets:
          - 'weft-network-1:9100'
          - 'weft-network-2:9100'
          - 'weft-network-3:9100'
```

The `job="weft-network"` label is what the **Replicas up** panel
counts ; renaming the job means editing the panel query to match.

## Dashboard contents

| Panel              | Metric                                  | Notes                                                                                |
| ------------------ | --------------------------------------- | ------------------------------------------------------------------------------------ |
| Etcd connected     | `weft_network_etcd_connected`           | Red on 0, green on 1.                                                                |
| Replicas up        | `count(up{job="weft-network"} == 1)`    | Orange below 3, red below 2 — 3-DC deployments need 2/3 for etcd quorum.             |
| Build info         | `weft_network_build_info`               | Table view ; confirms a rollout reached every replica.                               |
| RPC rate           | `rate(weft_network_rpc_total[5m])`      | Stacked per-method ; filtered by the `method` template variable.                     |
| Error rate         | non-`OK` / total                        | Threshold line at 5 % matches the alert policy in OBSERVABILITY.md.                  |
| p50 / p99 latency  | `histogram_quantile(…)`                 | Y axis in seconds ; p99 threshold line at 100 ms (control-plane SLO).                |

The `method` template variable (top of the dashboard) drives the RPC
rate, error rate, p50 and p99 panels — useful for zooming on a noisy
RPC during an incident.
