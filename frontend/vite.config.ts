import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  build: {
    // The output directory is the Go embed package and also contains gui.go.
    // run.sh clears the generated files before building.
    emptyOutDir: false,
  },
})
