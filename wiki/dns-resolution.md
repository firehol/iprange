# DNS resolution

When input files contain hostnames (one per line), `iprange` resolves them in parallel using a thread pool.

## Configuration

| Option | Default | Meaning |
|--------|---------|---------|
| `--dns-threads N` | 5 | Maximum number of parallel DNS queries |
| `--dns-silent` | off | Suppress all DNS error messages |
| `--dns-progress` | off | Show a progress bar during resolution |

## How it works

1. As each input line is parsed, hostnames are queued for resolution.
2. Worker threads pick requests from the queue and call `getaddrinfo()`.
3. Resolved IPs are added to a reply queue.
4. The main thread drains the reply queue periodically and after all requests finish.

Threads are created on demand up to `--dns-threads`. If the queue grows faster than threads can process, new threads are spawned up to the limit.

## Address family behavior

| Mode | Records resolved | Normalization |
|------|-----------------|---------------|
| IPv4 (default / `-4`) | A records only | None |
| IPv6 (`-6`) | AAAA and A records | A results mapped to `::ffff:x.x.x.x` |

In IPv6 mode, a hostname that has both AAAA and A records will contribute all addresses — IPv6 addresses directly, IPv4 addresses as IPv4-mapped IPv6.

## Retry and error handling

- **Temporary failures** (`EAI_AGAIN`): retried up to 20 times with 1-second delays between retry cycles.
- **Permanent failures** (`EAI_NONAME`, `EAI_FAIL`, etc.): logged to stderr and counted.
- **System errors** (`EAI_SYSTEM`, `EAI_MEMORY`): logged to stderr.

After all resolutions complete, if any hostname permanently failed, the entire load fails (returns error). Use `--dns-silent` to suppress the per-hostname error messages, but the load will still fail.

## Hostname detection

A line is treated as a hostname when:
- It contains only hostname-valid characters (alphanumeric, dot, hyphen, underscore)
- It does not look like a valid IP address or CIDR
- It appears alone on the line (optionally followed by a comment)

Lines that look like IPs but fail to parse are treated as errors, not hostnames. This prevents typos like `1.2.3.999` from triggering DNS resolution.

Hostnames cannot appear as range endpoints. A line like `host1.example.com - host2.example.com` is invalid.

## Performance notes

- With the default 5 threads, `iprange` can resolve hundreds of hostnames per second.
- For files with thousands of hostnames, increase `--dns-threads` (e.g., 50-100).
- DNS results are added to the ipset as they arrive, so resolution overlaps with continued file parsing.
- Each hostname resolution is independent — one slow or failing hostname does not block others.
