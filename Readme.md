# Apollo 🚀

> A highly scalable, language-agnostic job orchestration platform for long-running tasks

Apollo is a powerful jobs orchestrator designed for the real world. Whether you're running data migrations, database backups, or any other long-running tasks, Apollo provides a robust, scalable foundation that speaks any language.

## Why Apollo?

Ever tried running a massive data migration and had it fail halfway through? Or needed to schedule critical database backups across multiple environments? Apollo was built to solve these exact problems.

**Key Features:**
- 🐳 **Docker-native** - Run jobs in any language, any environment
- ☁️ **Cloud-ready** - Built for GCloud with Kubernetes support
- 🔄 **Reliable scheduling** - Cron-based repeatable jobs that actually work
- 🌐 **Language agnostic** - Define jobs in Go, Python, Node.js, or whatever you prefer
- 📊 **Real-time monitoring** - Track your jobs with detailed logs and status updates
- 🔧 **gRPC API** - Fast, type-safe communication

### Server Setup

```bash
curl https://apollo.lynxlab.tech/setup.sh | sudo bash
```

You'll be asked 

- Which deployment type you want (Cloud Run or Local Docker)
- Which port to expose the service on (default: 6910)
- Whether to log in to GHCR (GitHub Container Registry)
- Path to your jobs config file (optional)

### Client usage

[For Node click here](/proto/generated/node/proto/README.md)

> For all the langs you can generate from [Proto def](/proto/jobs.proto)

### Private API TLS

Mount the scheduler certificate and key as read-only secret files. Set `APOLLO_TLS_CERT_FILE` and `APOLLO_TLS_KEY_FILE`. The certificate must cover the hostname clients use. Clients must verify that hostname and trust the issuing CA. TLS 1.2 or newer is required; service-token and team authorization remain required.

Production rejects missing TLS configuration. Local fixtures can explicitly set `APOLLO_ALLOW_INSECURE=true` only outside production and without TLS files. The `--migrate-only` command does not start the API or require TLS.
