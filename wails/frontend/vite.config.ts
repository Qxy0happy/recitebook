import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import wails from "@wailsio/runtime/plugins/vite";

import lint from "./.oxlintrc.json" with { type: "json" };

// The `lint` key is read by `vp lint` / `vp check`; Vite itself ignores it.
// Passing a named object (instead of an inline literal) keeps the Vite config
// types intact while carrying the extra Vite+ key.
const config = {
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  plugins: [react(), tailwindcss(), wails("./bindings")],
  lint,
};

// https://vitejs.dev/config/
export default defineConfig(config);
