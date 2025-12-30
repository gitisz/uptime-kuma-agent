# Uptime Kuma Agent

A provisioning and metrics-push agent for Uptime Kuma. It creates monitor groups and push/HTTP monitors idempotently, retrieves push tokens, and (optionally) generates precise Telegraf drop-in configurations that forward system metrics to Uptime Kuma via short-lived containers.

## Features

- **Idempotent provisioning** – creates monitors and groups only if they don't already exist (matched by name).
- **Push token handling** – automatically fetches and persists push tokens after monitor creation.
- **Custom monitor support** – omits unsupported fields (e.g., conditions) to prevent Uptime Kuma API errors.
- **Telegraf integration** – generates per-monitor `[[outputs.exec]]` configs that:
  - Use exact `namepass`, `fieldpass`, and `tagpass` filtering for isolation and safety.
  - Run the agent itself in a short-lived Docker container to parse metrics and push to Uptime Kuma.
  - Include a dummy `[[outputs.discard]]` when needed to allow Telegraf startup without other outputs.
- **Multi-platform Docker images** – built for `amd64` and `arm64`.

## Building and Pushing Docker Image

To build and push the multi-platform Docker image for developers:

```bash
docker buildx build --push --platform linux/amd64,linux/arm64 -t <docker-registry>.com/uptime-kuma-agent:latest .
```

## CLI Usage

```bash
Usage:
  uptime-kuma-agent [flags]
  uptime-kuma-agent [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  push-metric One-shot push triggered by Telegraf inputs.execd

Flags:
      --config string         path to config file (default "/config/config.yaml")
  -h, --help                  help for uptime-kuma-agent
      --telegraf-dir string   Directory to write Telegraf drop-in configs (default "/telegraf.d")
      --with-telegraf         generate Telegraf configuration files (default true)

Use "uptime-kuma-agent [command] --help" for more information about a command.

```

## Config

Edit `config/config.yaml` (from [`config.yaml.example`](./config.yaml.example)).

Example:

```yaml
uptime_kuma_url: "https://<your-uptime-kuma-fqdn>"
username: "administrator"
password: "your_password_or_api_key"  # Better: use API key if enabled

group_name: "{{ .HostName }} Monitors"  # Template with VM hostname
interval: 60
max_retries: 1

# Global agent behavior
agent:
  use_outputs_discard: true   # When true: adds [[outputs.discard]] to each drop-in so Telegraf starts even with only execd
                              # When false: no dummy output — use when you have real outputs elsewhere (e.g., InfluxDB, Prometheus)
  docker_image: "<docker-registry>/uptime-kuma-agent:latest" # Registry for the Docker image

monitors:
  - type: push
    name: "{{ .HostName }} - CPU %"
    threshold: 90
    metric: cpu
    field: usage_user
    threshold: 90

  # RAM - used_percent from mem input
  - type: push
    name: "{{ .HostName }} - RAM %"
    threshold: 90
    metric: mem
    field: used_percent

  # Root Disk - disk input, filter by filesystem path
  - type: push
    name: "{{ .HostName }} - Root Disk %"
    threshold: 85
    metric: disk
    field: used_percent
    filesystem: "/"

  # Data Disk - custom mount point
  - type: push
    name: "{{ .HostName }} - Data Disk %"
    threshold: 85
    metric: disk
    field: used_percent
    filesystem: "/mnt/data/uptime-kuma-test"

  # HTTP health check (no metric mapping needed)
  - type: http
    name: "{{ .HostName }} - Web"
    url: "http://localhost:8080/health"

```

# Telegraf Available Fields by Measurement

Below is a breakdown of some Telegraf metrics. For a full list of available Telegraf metrics, see [Telegraf Input Plugins](https://docs.influxdata.com/telegraf/v1/plugins/inputs/).

_Note: only a few metrics have been tested. If you require additional configurations, please raise a GitHub Issue._

### `cpu` (CPU Usage — multiple series: per-core + `cpu-total`)

**Useful fields for percentage monitoring:**
- `usage_user` → User CPU % (most common for "CPU usage")
- `usage_system` → System CPU %
- `usage_idle` → Idle %
- `usage_iowait` → Waiting for I/O %
- `usage_irq` → Interrupt handling %
- `usage_softirq` → Soft interrupt %
- `usage_nice` → Nice-priority processes %
- `usage_steal` → Stolen by hypervisor %
- `usage_guest` / `usage_guest_nice` → Guest VM CPU usage

**Recommended for monitoring:**
`usage_user` (as you're currently using) or `usage_user + usage_system` combined.

**Note:** Use with tag `cpu=cpu-total` for aggregated system-wide value (your `[[inputs.cpu]] totalcpu = true` enables this).

### `mem` (Memory Usage)

**All available fields:**
- `active` → Memory currently in use
- `available` → Memory available for new processes
- `available_percent` → % available (recommended alternative)
- `buffered` → Buffered memory
- `cached` → Cached memory
- `commit_limit` → Max committable memory
- `committed_as` → Currently committed memory
- `dirty` → Dirty pages
- `free` → Free memory
- `high_free` / `high_total` → High memory zone
- `huge_page_size` / `huge_pages_free` / `huge_pages_total` → Huge pages
- `inactive` → Inactive memory
- `mapped` → Mapped memory
- `page_tables` → Memory used for page tables
- `shared` → Shared memory
- `slab` → Kernel slab allocator
- `sreclaimable` / `sunreclaim` → Slab reclaimable/unreclaimable
- `swap_cached` / `swap_free` / `swap_total` → Swap cache
- `total` → Total physical memory
- `used` → Used memory (raw bytes)
- `used_percent` → **Used memory %** ← **You are using this correctly**
- `vmalloc_chunk` / `vmalloc_total` / `vmalloc_used` → Virtual memory allocation
- `write_back` / `write_back_tmp` → Pages being written back

**Recommended:** `used_percent` (your current choice) or `available_percent`

### `disk` (Disk Space Usage)

**All available fields:**
- `free` → Free bytes
- `inodes_free` → Free inodes
- `inodes_total` → Total inodes
- `inodes_used` → Used inodes
- `inodes_used_percent` → % inodes used
- `total` → Total bytes
- `used` → Used bytes
- `used_percent` → **Used space %** ← **You are using this correctly**

**Important tag:** `path` (e.g., `/`, `/mnt/data/uptime-kuma-test`) — filtered via `tagpass`

### `diskio` (Disk I/O Activity)

**All available fields:**
- `io_time` → Time spent doing I/O
- `iops_in_progress` → Current IOPS in progress
- `merged_reads` / `merged_writes` → Merged operations
- `read_bytes` / `write_bytes` → Bytes read/written
- `read_time` / `write_time` → Time spent reading/writing
- `reads` / `writes` → Number of read/write operations
- `weighted_io_time` → Weighted I/O time

**Tag:** `name` (device name, e.g., `vda`, `vda1`, `vdb1`)

### `system` (System Load & Uptime)

**All available fields:**
- `load1`, `load5`, `load15` → Load averages
- `n_cpus`, `n_physical_cpus` → CPU count
- `n_unique_users`, `n_users` → Logged-in users
- `uptime` → Seconds since boot
- `uptime_format` → Human-readable uptime (e.g., "4:06")

### `swap` (Swap Usage)

**All available fields:**
- `free`, `total`, `used`, `used_percent` → Swap space usage
- `in`, `out` → Pages swapped in/out

### `processes` (Process States)

**All available fields:**
- `blocked`, `dead`, `idle`, `paging`, `running`, `sleeping`, `stopped`, `total`, `total_threads`, `unknown`, `zombies`

### `kernel` (Kernel Stats)

**All available fields:**
- `boot_time` → Unix timestamp of boot
- `context_switches` → Total context switches
- `entropy_avail` → Available entropy
- `interrupts` → Total interrupts
- `processes_forked` → Total processes forked

### Summary: Your Current Choices Are Optimal

| Monitor        | Measurement | Field Used         | Correct? | Notes |
|----------------|-------------|--------------------|----------|-------|
| CPU %          | `cpu`       | `usage_user`       | Yes     | Best for user CPU load |
| RAM %          | `mem`       | `used_percent`     | Yes     | Standard and accurate |
| Root/Disk %    | `disk`      | `used_percent`     | Yes     | With `path` tag filter |

**No changes needed** — your `config.yaml` and filtering are already using the best and correct fields.

## Deployment Tips

 - Mount your edited config.yaml to /config/config.yaml in the container.
 - Mount the host's Telegraf drop-in directory (usually /etc/telegraf/telegraf.d) to /telegraf.d.
 - Restart Telegraf after the agent runs (or send SIGHUP).
 - For testing disk usage: sudo fallocate -l 5G /mnt/data/uptime-kuma-test/test.bin

You’re good to go! 🚀
