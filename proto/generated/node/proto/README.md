# @synehq/apollo

Apollo is a jobs orchestrator for Docker/GCloud based jobs and scheduler. This package is a Node.js client for job scheduling and management that connects to your Apollo gRPC server.

## Installation

```bash
bun add @synehq/apollo
```

## Usage Guide

### Importing the Client

```ts
import { credentials } from "@grpc/grpc-js";
import { jobs } from "@synehq/apollo";

// Connect to your Apollo gRPC server
const client = new jobs.JobsServiceClient(
  process.env.APOLLO_ADDR ?? "localhost:6910",
  credentials.createInsecure()
);
```

### Run a One-time Job

```ts
const request = jobs.RunJobRequest.fromObject({
  name: "demo-once",
  job_id: "unique-job-id", // optional unique identifier
  command: "docker://ghcr.io/owner/image:tag",
  args_base64: Buffer.from(JSON.stringify({ foo: "bar" })).toString("base64"),
  resources: jobs.Resources.fromObject({ 
    cpu: "500m", 
    memory: "256Mi" 
  }),
  type: jobs.JobType.JOB_TYPE_ONE_TIME,
  overrides: jobs.JobOverrides.fromObject({
    args: ["--custom-arg", "value"],
    env: [
      jobs.EnvVar.fromObject({ name: "NODE_ENV", value: "production" }),
      jobs.EnvVar.fromObject({ name: "API_KEY", value: "secret" })
    ],
    resources: jobs.Resources.fromObject({ cpu: "1000m", memory: "512Mi" }),
    task_count: 2
  })
});

client.RunJob(request, (err, res) => {
  if (err) {
    console.error("RunJob error", err);
    return;
  }
  console.log("RunJob id:", res?.id);
  console.log("RunJob logs:", res?.logs);
});
```

### Create/Update a Repeatable Schedule

```ts
const scheduleReq = jobs.UpdateScheduleRequest.fromObject({
  name: "nightly-report",
  schedule: "0 2 * * *" // cron expression
});

client.UpdateSchedule(scheduleReq, (err) => {
  if (err) {
    console.error("UpdateSchedule error", err);
    return;
  }
  console.log("Schedule updated");
});
```

### List Schedules

```ts
client.ListSchedules(new jobs.ListSchedulesRequest(), (err, res) => {
  if (err) {
    console.error("ListSchedules error", err);
    return;
  }
  
  // Each item is a ScheduleItem with name, command, args_base64, cron, and resources
  res?.items?.forEach(item => {
    console.log(`Schedule: ${item.name}`);
    console.log(`Command: ${item.command}`);
    console.log(`Cron: ${item.cron}`);
    console.log(`Resources:`, item.resources?.toObject());
    console.log("---");
  });
});
```

### Delete a Job

```ts
client.DeleteJob(
  jobs.DeleteJobRequest.fromObject({ name: "demo-once" }),
  (err) => {
    if (err) {
      console.error("DeleteJob error", err);
      return;
    }
    console.log("Job deleted");
  }
);
```

## API Reference

### Classes

#### `RunJobRequest`

Main request class for running jobs with the following fields:

- `name` (string): Job name
- `job_id` (string, optional): Unique job identifier
- `command` (string): Docker image or command to run
- `args_base64` (string): Base64 encoded arguments
- `resources` (Resources): CPU and memory requirements
- `type` (JobType): Either `JOB_TYPE_ONE_TIME` or `JOB_TYPE_REPEATABLE`
- `schedule` (string): Cron expression for repeatable jobs
- `overrides` (JobOverrides, optional): Runtime overrides

#### `JobOverrides`

Runtime overrides for job execution:

- `args` (string[]): Additional command line arguments
- `env` (EnvVar[]): Environment variables
- `resources` (Resources): Override resource requirements
- `task_count` (number): Number of parallel tasks

#### `EnvVar`

Environment variable definition:

- `name` (string): Variable name
- `value` (string): Variable value

#### `Resources`

Resource requirements:

- `cpu` (string): CPU requirement (e.g., "500m", "1")
- `memory` (string): Memory requirement (e.g., "256Mi", "1Gi")

#### `ScheduleItem`

Scheduled job information:

- `name` (string): Schedule name
- `command` (string): Command to execute
- `args_base64` (string): Base64 encoded arguments
- `cron` (string): Cron expression
- `resources` (Resources): Resource requirements

### Job Types

- `jobs.JobType.JOB_TYPE_ONE_TIME`: Execute once
- `jobs.JobType.JOB_TYPE_REPEATABLE`: Execute on schedule

## Advanced Examples

### Running a Job with Environment Variables and Overrides

```ts
const request = jobs.RunJobRequest.fromObject({
  name: "data-processing",
  job_id: "proc-2024-01-15",
  command: "docker://myregistry/data-processor:v1.2",
  args_base64: Buffer.from(JSON.stringify({
    inputPath: "/data/input",
    outputPath: "/data/output"
  })).toString("base64"),
  resources: jobs.Resources.fromObject({
    cpu: "1000m",
    memory: "2Gi"
  }),
  type: jobs.JobType.JOB_TYPE_ONE_TIME,
  overrides: jobs.JobOverrides.fromObject({
    args: ["--verbose", "--parallel"],
    env: [
      jobs.EnvVar.fromObject({ name: "DEBUG", value: "true" }),
      jobs.EnvVar.fromObject({ name: "LOG_LEVEL", value: "info" }),
      jobs.EnvVar.fromObject({ name: "DATABASE_URL", value: process.env.DATABASE_URL })
    ],
    resources: jobs.Resources.fromObject({
      cpu: "2000m",
      memory: "4Gi"
    }),
    task_count: 3
  })
});

client.RunJob(request, (err, res) => {
  if (err) {
    console.error("Job execution failed:", err);
    return;
  }
  console.log(`Job ${res?.id} started successfully`);
  console.log("Initial logs:", res?.logs);
});
```

### Creating a Repeatable Job with Schedule

```ts
const repeatableJob = jobs.RunJobRequest.fromObject({
  name: "daily-cleanup",
  command: "docker://myregistry/cleanup:v1.0",
  args_base64: Buffer.from(JSON.stringify({
    retentionDays: 30,
    dryRun: false
  })).toString("base64"),
  resources: jobs.Resources.fromObject({
    cpu: "500m",
    memory: "512Mi"
  }),
  type: jobs.JobType.JOB_TYPE_REPEATABLE,
  schedule: "0 2 * * *" // Daily at 2 AM
});

client.RunJob(repeatableJob, (err, res) => {
  if (err) {
    console.error("Failed to create repeatable job:", err);
    return;
  }
  console.log(`Repeatable job created with ID: ${res?.id}`);
});
```
