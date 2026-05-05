const esbuild = require("esbuild");
const { execSync } = require("child_process");
const path = require("path");
const fs = require("fs");

const twBin = path.join(__dirname, "node_modules", ".bin", "tailwindcss");
const tmpCss = path.join(__dirname, ".tailwind-tmp.css");
const outPath = path.join(__dirname, "../cmd/rbite/web/index.html");

function buildCss() {
  execSync(`"${twBin}" -i src/input.css -o "${tmpCss}"`, { cwd: __dirname, stdio: "pipe" });
  const css = fs.readFileSync(tmpCss, "utf8");
  try { fs.unlinkSync(tmpCss); } catch {}
  return css;
}

function writeHtml(js, css) {
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
<script>${js}</script>
</body>
</html>`;
  fs.mkdirSync(path.dirname(outPath), { recursive: true });
  fs.writeFileSync(outPath, html, "utf8");
  console.log("[web] rebuilt " + outPath + " (" + Buffer.byteLength(html) + " bytes)");
}

(async () => {
  const ctx = await esbuild.context({
    entryPoints: [path.join(__dirname, "src/index.jsx")],
    bundle: true,
    minify: false,
    jsxFactory: "h",
    jsxFragment: "Fragment",
    format: "iife",
    write: false,
    plugins: [{
      name: "html-assembler",
      setup(build) {
        build.onEnd(result => {
          if (result.errors.length === 0) {
            const css = buildCss();
            writeHtml(result.outputFiles[0].text, css);
          }
        });
      },
    }],
  });

  await ctx.watch();
  console.log("[web] watching web/src/ for changes...");
})();
