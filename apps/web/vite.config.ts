import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { fumadocsMdx } from "fumadocs-mdx/vite";
import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [fumadocsMdx({ index: false }), react(), tailwindcss()],
  build: { target: "es2022" },
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
    // Repository Markdown lives outside apps/web; resolve its JSX from this app.
    dedupe: [
      "react",
      "react-dom",
      "fumadocs-core",
      "fumadocs-ui",
      "fumadocs-mdx",
    ],
  },
  server: { host: "0.0.0.0", port: 5173 },
});
