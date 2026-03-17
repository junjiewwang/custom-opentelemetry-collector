import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'path'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  // 生产构建时资源路径前缀，与 Go 后端的 /ui/ 挂载路径对应
  base: '/ui/',
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  // 构建输出到 dist 目录，用于 Go embed
  build: {
    outDir: 'dist',
    // 生成资源到 assets 子目录
    assetsDir: 'assets',
    // 生成 sourcemap 方便调试
    sourcemap: false,
    // 清除输出目录
    emptyOutDir: true,
    // 代码分割：将大型依赖单独打包
    rollupOptions: {
      output: {
        manualChunks: {
          'vendor-react': ['react', 'react-dom', 'react-router-dom'],
          'vendor-echarts': ['echarts', 'echarts-for-react'],
        },
      },
    },
    // ECharts 单独打包后约 609KB（含 Graph 图表，gzip 205KB），提高阈值
    chunkSizeWarningLimit: 650,
  },
  // 开发时代理 API 请求到 Go 后端
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8088',
        changeOrigin: true,
      },
      // 代理 WebSocket
      '/api/v2/arthas/ws': {
        target: 'ws://localhost:8088',
        ws: true,
      },
      // 代理旧前端（Legacy）
      '/legacy': {
        target: 'http://localhost:8088',
        changeOrigin: true,
      },
    },
  },
})
