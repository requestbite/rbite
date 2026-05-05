const esbuild = require("esbuild");
const { execSync } = require("child_process");
const path = require("path");
const fs = require("fs");

const twBin = path.join(__dirname, "node_modules", ".bin", "tailwindcss");
const tmpCss = path.join(__dirname, ".tailwind-tmp.css");

execSync(`"${twBin}" -i src/input.css -o "${tmpCss}" --minify`, {
  cwd: __dirname,
  stdio: "inherit",
});
const css = fs.readFileSync(tmpCss, "utf8");
fs.unlinkSync(tmpCss);

const result = esbuild.buildSync({
  entryPoints: [path.join(__dirname, "src/index.jsx")],
  bundle: true,
  minify: true,
  jsxFactory: "h",
  jsxFragment: "Fragment",
  format: "iife",
  write: false,
});

const js = result.outputFiles[0].text;

const html = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>RequestBite Tunnel</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600&family=JetBrains+Mono&display=swap" rel="stylesheet">
<style>${css}</style>
</head>
<body>
<div id="app"></div>
<script>window.__WRITABLE__={{WRITABLE}};</script>
<script>${js}</script>
</body>
</html>`;

const outPath = path.join(__dirname, "../cmd/rbite/web/index.html");
fs.mkdirSync(path.dirname(outPath), { recursive: true });
fs.writeFileSync(outPath, html, "utf8");
console.log("Built: " + outPath + " (" + Buffer.byteLength(html) + " bytes)");
