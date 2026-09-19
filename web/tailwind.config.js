/** @type {import('tailwindcss').Config} */
export default {
  content: [
    "./index.html",
    "./src/**/*.{js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      colors: {
        dark: {
          900: '#090d13',
          800: '#0d1117',
          700: '#161b22',
          600: '#21262d',
          500: '#30363d',
          400: '#484f58',
          300: '#8b949e',
          200: '#c9d1d9',
          100: '#f0f6fc',
        },
        ops: {
          green: '#238636',
          brightGreen: '#3fb950',
          blue: '#1f6feb',
          brightBlue: '#58a6ff',
          amber: '#9e6a03',
          brightAmber: '#e3b341',
          red: '#da3633',
          brightRed: '#f85149',
          purple: '#8957e5',
          brightPurple: '#bc8cff',
        }
      },
      fontFamily: {
        mono: ['ui-monospace', 'SFMono-Regular', 'Menlo', 'Monaco', 'Consolas', 'monospace'],
        sans: ['ui-sans-serif', 'system-ui', '-apple-system', 'BlinkMacSystemFont', '"Segoe UI"', 'Roboto', 'sans-serif'],
      },
    },
  },
  plugins: [],
}
