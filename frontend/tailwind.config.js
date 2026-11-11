/** @type {import('tailwindcss').Config} */
export default {
  darkMode: 'class',
  content: [
    "./index.html",
    "./src/**/*.{vue,js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      fontFamily: {
        serif: [
          'Source Serif 4', 'Georgia', 'Noto Serif SC', 'Songti SC', 'STSong', 'SimSun', 'serif',
        ],
        sans: [
          'Inter', '-apple-system', 'BlinkMacSystemFont', 'PingFang SC', 'Microsoft YaHei', 'Arial', 'sans-serif',
        ],
      },
      colors: {
        // 跟随当前皮肤（其余颜色都由 style.css / skins.css 的变量决定）
        ink: {
          DEFAULT: 'var(--ink)',
        },
      },
    },
  },
  plugins: [],
}
