# @synehq/apollo

Apollo is a jobs orchestrator for Docker/GCloud based jobs and scheduler. This package is a Node.js client for job scheduling and management that connects to your Apollo gRPC server.

## Installation

```
bun add @synehq/apollo
```

## Usage Guide

### Importing the Client

```ts
import { credentials } from "@grpc/grpc-js";
import { jobs } from "@synehq/apollo";

// Connect to your Apollo gRPC server
const client = new jobs.JobsServiceClient(
  process.env.APOLLO_ADDR ?? "localhost:50051",
  credentials.createInsecure()
);
```

### Run a One-time Job

```ts
const request = jobs.RunJobRequest.fromObject({
  name: "demo-once",
  command: "docker://ghcr.io/owner/image:tag",
  args_base64: Buffer.from(JSON.stringify({ foo: "bar" })).toString("base64"),
  resources: { cpu: "500m", memory: "256Mi" },
  type: jobs.JobType.JOB_TYPE_ONE_TIME
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
  schedule: "0 2 * * *" // cron
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
  console.table(res?.items?.map(i => i.toObject()));
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
