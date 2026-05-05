/** @type {import('tailwindcss').Config} */
module.exports = {
  content: ["./src/**/*.{js,jsx}"],
  theme: {
    extend: {
      fontFamily: {
        jetbrains: ['"JetBrains Mono"', "monospace"],
      },
    },
  },
};
