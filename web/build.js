const esbuild = require("esbuild");
const path = require("path");
const fs = require("fs");

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

const css = `
*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
body{background:#0f1117;color:#e2e8f0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;font-size:14px;line-height:1.6}
a{color:#63b3ed;text-decoration:none}
a:hover{text-decoration:underline}
.container{max-width:860px;margin:0 auto;padding:24px 16px}
header{margin-bottom:24px}
h1{font-size:20px;font-weight:600;color:#f7fafc;margin-bottom:10px}
h1 .logo{margin-right:6px}
.breadcrumb{display:flex;flex-wrap:wrap;gap:4px;align-items:center;font-size:13px;color:#718096}
.breadcrumb .sep{margin:0 2px;color:#4a5568}
.breadcrumb .current{color:#e2e8f0;font-weight:500}
table{width:100%;border-collapse:collapse;background:#1a1d27;border-radius:8px;overflow:hidden}
thead tr{background:#252836}
th{padding:10px 16px;text-align:left;font-size:12px;font-weight:600;color:#a0aec0;text-transform:uppercase;letter-spacing:.05em}
tbody tr{border-top:1px solid #252836}
tbody tr:hover{background:#1e2130}
td{padding:10px 16px}
.name-cell{min-width:200px}
.size-cell,.date-cell{color:#718096;font-size:13px;white-space:nowrap}
.dir{font-weight:500}
.file{color:#90cdf4}
.error{background:#2d1f1f;border:1px solid #c53030;color:#fc8181;padding:12px 16px;border-radius:6px;margin-bottom:16px}
.loading,.empty{color:#718096;padding:32px 16px;text-align:center}
`;

const html = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>rbite file share</title>
<style>${css}</style>
</head>
<body>
<div id="app"></div>
<script>${js}</script>
</body>
</html>`;

const outPath = path.join(__dirname, "../cmd/rbite/web/index.html");
fs.mkdirSync(path.dirname(outPath), { recursive: true });
fs.writeFileSync(outPath, html, "utf8");
console.log("Built: " + outPath + " (" + Buffer.byteLength(html) + " bytes)");
