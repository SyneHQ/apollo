# Production startup

- Set `PORT=6910`, a 32-character or longer `APOLLO_SERVICE_TOKEN`, and `METADATA_DATABASE_URL`.
- Set `ENVIRONMENT=production`. Use `STORE_DRIVER=postgres` with a dedicated `STORE_PATH` connection URL, or `sqlite` with an absolute path on a persistent volume.
- Apply PostgreSQL schema changes in a separate job: `APOLLO_MIGRATION_DATABASE_URL=... /app/main --migrate-only`. This job does not start providers, listeners or schedules.
- Give the production scheduler DML access to `apollo_jobs` and `apollo_executions`, plus SELECT on `apollo_schema_receipts`. Runtime startup checks the schema version and required columns without DDL.
- Give the web app SELECT-only access to `apollo_executions` through `APOLLO_DATABASE_URL`. Keep migration credentials out of both runtimes.
- Run one scheduler replica with Recreate updates. PostgreSQL storage alone does not provide leader election.
- If Infisical is enabled, set its HTTPS URL, client ID, client secret, project ID, and environment. A partial configuration or failed secret load stops startup. Provider error bodies are not logged.
- Startup verifies metadata connectivity and schedule storage before it starts jobs or accepts requests. Invalid stored schedules stop startup and require operator repair.
- Keep the gRPC listener on a private service network. Requests still require the service token, user identity, team membership, and current job ownership.
- `JOBS_PROVIDER=local` requires a separately secured Docker daemon. Do not mount the Hakopod host's Docker socket. Native Hakopod jobs use the provider described below.
- Scheduled executions of the same job do not overlap. Invalid or failed schedule replacements retain the previous schedule. Execution history keeps a distinct ID for each run.

## Validation

```sh
# Use a disposable PostgreSQL database; tests create temporary tables and schemas.
SECURITY_TEST_DATABASE_URL='postgres://USER:PASSWORD@HOST/TEST_DB?sslmode=require' \
  go test -count=1 -race -p 1 . ./keys ./scheduler ./server ./cmd
```

Without the test URL, PostgreSQL integration checks skip. Unit tests do not qualify worker images or a production deployment.

## Native Hakopod jobs

Set `JOBS_PROVIDER=hakopod` and mount a reviewed [jobs file](examples/hakopod-jobs.yml) at `/app/jobs.yml`. Supply `APOLLO_HAKOPOD_API_KEY` through the secret provider; its machine identity needs only `jobs:invoke`, `jobs:read`, `jobs:cancel`, and `jobs:logs` on this application and must be listed by each job template.

Each template fixes its deployment revision, image digest, command, resources and secret grants. `request_image` optionally maps an existing authenticated image alias to that digest. Worker execution always uses the fixed template; caller resource hints cannot exceed its ceiling. TLS verification is mandatory; a private CA can be supplied with `APOLLO_HAKOPOD_CA_PEM`.

The worker command must read `/run/hakopod/invocation/input.json` directly. Use a payload-file-enabled Rover image. Flowr and connector workers remain unsupported by this provider until their own file-input contracts are qualified. Keep `APOLLO_CONNECTORS_ENABLED=false`.

Run one Apollo scheduler replica. Hakopod dispatch, cancellation, cleanup and bounded terminal logs use its durable invocation API. Lost submission acknowledgements are looked up by idempotency key; Apollo does not create a replacement job. A cancellation that cannot be confirmed is returned as an error.
