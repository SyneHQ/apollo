# Production startup

- Set `PORT=6910`, a 32-character or longer `APOLLO_SERVICE_TOKEN`, and `METADATA_DATABASE_URL`.
- Set `ENVIRONMENT=production`. Use `STORE_DRIVER=postgres` with a dedicated `STORE_PATH` connection URL, or `sqlite` with an absolute path on a persistent volume.
- Run one scheduler replica. PostgreSQL storage alone does not provide leader election.
- If Infisical is enabled, set its HTTPS URL, client ID, client secret, project ID, and environment. A partial configuration or failed secret load stops startup. Provider error bodies are not logged.
- Startup verifies metadata connectivity and schedule storage before it starts jobs or accepts requests. Invalid stored schedules stop startup and require operator repair.
- Keep the gRPC listener on a private service network. Requests still require the service token, user identity, team membership, and current job ownership.
- `JOBS_PROVIDER=local` requires a separately secured Docker daemon. Do not mount the Hakopod host's Docker socket. Native Hakopod invocation support is separate work; these startup fixes do not provide it.
- Scheduled executions of the same job do not overlap. Invalid or failed schedule replacements retain the previous schedule. Execution history keeps a distinct ID for each run.

## Validation

```sh
# Use a disposable PostgreSQL database; tests create temporary tables and schemas.
SECURITY_TEST_DATABASE_URL='postgres://USER:PASSWORD@HOST/TEST_DB?sslmode=require' \
  go test -count=1 -race -p 1 . ./keys ./scheduler ./server ./cmd
```

Without the test URL, PostgreSQL integration checks skip. Unit tests do not qualify worker images or a production deployment.
