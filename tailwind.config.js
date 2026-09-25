/** @type {import('tailwindcss').Config} */
module.exports = {
  content: ["./web/templates/**/*.html", "./web/static/app.js", "./internal/server/templates.go"],
  theme: {
    extend: {
      fontFamily: { sans: ["Inter", "ui-sans-serif", "system-ui", "sans-serif"] },
    },
  },
  plugins: [require("@tailwindcss/forms")],
};
