// Builds the app into build/:
//   build/renderer/  the operator page from linux/web (`npm run build:operator`)
//   build/main.js    main process (bundled, CommonJS)
//   build/preload.js preload (bundled: sandboxed preloads can only require electron)
// SKIP_WEB=1 reuses an existing ../web/dist-operator.
import { build } from "esbuild";
import { execFileSync } from "node:child_process";
import { cpSync, existsSync, rmSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const web = join(root, "..", "web");
const out = join(root, "build");

if (!process.env.SKIP_WEB) {
  if (!existsSync(join(web, "node_modules"))) execFileSync("npm", ["ci"], { cwd: web, stdio: "inherit" });
  execFileSync("npm", ["run", "build:operator"], { cwd: web, stdio: "inherit" });
}
const dist = join(web, "dist-operator");
if (!existsSync(join(dist, "operator.html"))) throw new Error(`no operator build in ${dist}`);

rmSync(out, { recursive: true, force: true });
cpSync(dist, join(out, "renderer"), { recursive: true });
cpSync(join(root, "resources", "icon.png"), join(out, "icon.png")); // tray icon

const common = {
  bundle: true,
  platform: "node",
  format: "cjs",
  target: "node24",
  external: ["electron"],
  sourcemap: false,
  logLevel: "warning",
  define: { "process.env.NODE_ENV": '"production"' },
};
await build({ ...common, entryPoints: [join(root, "src", "main.ts")], outfile: join(out, "main.js") });
await build({ ...common, entryPoints: [join(root, "src", "preload.ts")], outfile: join(out, "preload.js") });
console.log("built", out);
