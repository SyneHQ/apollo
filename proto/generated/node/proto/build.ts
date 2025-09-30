import type { BuildConfig } from "bun";
import dts from "bun-plugin-dts";

// List your external dependencies here (as in package.json "dependencies" and "peerDependencies")
const externals = [
  "google-protobuf",
  "@grpc/grpc-js"
];

const defaultBuildConfig: BuildConfig = {
  entrypoints: ["./jobs.ts"],
  outdir: "./dist",
  target: "node",
  external: externals,
};

await Promise.all([
  Bun.build({
    ...defaultBuildConfig,
    plugins: [dts()],
    format: "esm",
    naming: "[dir]/[name].js",
  }).catch(() => {}),
  Bun.build({
    ...defaultBuildConfig,
    format: "cjs",
    naming: "[dir]/[name].cjs",
  }).catch(() => {}),
]);
