import { copyFile, mkdir, readFile, rm } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(fileURLToPath(import.meta.url));
const output = join(root, "dist");
const html = await readFile(join(root, "index.html"), "utf8");
if (!html.includes("Your agents.") || html.includes('id="root"')) {
  throw new Error("Expected the standalone product vitrine, not the console.");
}
await rm(output, { recursive: true, force: true });
await mkdir(join(output, "fonts"), { recursive: true });
// An explicit allowlist keeps the public site independent of app data and secrets.
for (const name of ["index.html", "site.css", "icon.svg", "robots.txt", "sitemap.xml"]) {
  await copyFile(join(root, name), join(output, name));
}
const font = join(root, "../web/node_modules/@fontsource-variable/geist");
await copyFile(join(font, "files/geist-latin-wght-normal.woff2"), join(output, "fonts/geist-latin-wght-normal.woff2"));
await copyFile(join(font, "LICENSE"), join(output, "fonts/OFL.txt"));
console.log("Built Circular's public vitrine: 7 static files, no application bundle.");
