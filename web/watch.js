const esbuild = require("esbuild");
const path = require("path");
const fs = require("fs");

const outPath = path.join(__dirname, "../cmd/rbite/web/index.html");

const css = `
*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
body{background:#ffffff;color:#111827;font-family:'Inter',-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;font-size:14px;line-height:1.6;min-height:100vh;padding:32px 16px}
a{text-decoration:none}
a:hover{text-decoration:underline}
.page{max-width:880px;margin:0 auto}
.site-title{font-family:'JetBrains Mono',monospace;font-size:11px;color:#9ca3af;margin-bottom:16px;letter-spacing:0.04em}
.browser-card{background:#1e2028;border-radius:12px;overflow:hidden;box-shadow:0 4px 24px rgba(0,0,0,0.08)}
header{background:#181b24;padding:16px 20px;border-bottom:1px solid #252836}
.breadcrumb{display:flex;flex-wrap:wrap;gap:4px;align-items:center;font-family:'Inter',sans-serif;font-size:13px;color:#64748b}
.breadcrumb .sep{margin:0 2px;color:#374151}
.breadcrumb .current{color:#e2e8f0;font-weight:500}
.breadcrumb a{color:#60a5fa}
.breadcrumb a:hover{text-decoration:underline}
.table-wrap{overflow-x:auto}
table{width:100%;border-collapse:collapse;font-family:'Inter',sans-serif}
thead tr{background:#252836}
th{padding:10px 20px;text-align:left;font-size:11px;font-weight:600;color:#94a3b8;text-transform:uppercase;letter-spacing:.06em;white-space:nowrap}
tbody tr{border-top:1px solid #252836;color:#e2e8f0}
tbody tr:hover{background:#252836}
td{padding:12px 20px}
.name-cell{min-width:160px}
.size-cell,.date-cell{color:#64748b;font-size:13px;white-space:nowrap}
.dir{color:#e2e8f0;font-weight:500}
.file{color:#60a5fa}
.error{background:#2d1f1f;border:1px solid #dc2626;color:#fca5a5;padding:12px 20px;margin:16px;border-radius:6px}
.loading,.empty{color:#64748b;padding:48px 20px;text-align:center;font-family:'Inter',sans-serif}
@media(max-width:600px){
body{padding:16px 12px}
.site-title{font-size:10px}
th{padding:8px 14px}
td{padding:10px 14px}
.col-date{display:none}
}
`;

function writeHtml(js) {
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
            writeHtml(result.outputFiles[0].text);
          }
        });
      },
    }],
  });

  await ctx.watch();
  console.log("[web] watching web/src/ for changes...");
})();
