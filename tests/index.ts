import { jobs } from "@synehq/apollo";
import { credentials } from "@grpc/grpc-js";

const client = new jobs.JobsServiceClient(
  "localhost:6910",
  credentials.createInsecure()
);

const request = jobs.RunJobRequest.fromObject({
  name: "test-job",
  prefix: "/app/rover",
  image: "ghcr.io/synehq/rover.ts:sudo",
  command: "handleBackupJob",
  args_base64: Buffer.from(
    JSON.stringify({
      backupScheduleId: "cmjcfctiv0006pb42mn8gze5w",
      sendOnEmail: true,
    })
  ).toString("base64"),
  type: jobs.JobType.JOB_TYPE_ONE_TIME,
  resources: jobs.Resources.fromObject({
    cpu: "500m",
    memory: "1Gi",
  }),
});

client.RunJob(request, (err, res) => {
  if (err) {
    console.error("RunJob error", err);
    return null;
  }
  return res;
});
