import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

/**
 * Wails 桌面端前端构建配置。
 *
 * 两个关键点：
 *  1. `base: './'` —— Wails 通过自定义协议（不是 http 根路径）加载 frontend/dist，
 *     产物里的资源引用必须是**相对路径**，否则白屏。
 *  2. `public/shim.js` 会被原样复制到 dist 根目录。它是把 window.bastion 这层接口
 *     翻译到 Go 后端 WebSocket/HTTP 的适配层，必须在应用 bundle **之前**加载
 *     （见 index.html 里的 <script src="/shim.js">）。
 */
export default defineConfig({
  plugins: [react()],
  base: './',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // 界面是要打进 exe 的，体积比首屏加载时间重要不了多少，但也没必要压得太狠
    chunkSizeWarningLimit: 1500
  }
})
