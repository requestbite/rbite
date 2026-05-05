module.exports = `
*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
body{background:#f9fafb;color:#111827;font-family:'Inter',-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;font-size:14px;line-height:1.6;min-height:100vh;padding:32px 16px}
a{text-decoration:none}
.page{max-width:880px;margin:0 auto}
.page-header{display:flex;flex-direction:column;align-items:center;margin-bottom:20px;gap:10px}
.site-title{font-family:'JetBrains Mono',monospace;font-size:14px;color:#9ca3af;letter-spacing:0.04em;text-align:center}
.browser-card{background:#ffffff;border-radius:8px;overflow:hidden;outline:1px solid #d1d5db;outline-offset:-1px}
header{background:#ffffff;padding:10px 16px;border-bottom:1px solid #e5e7eb}
.breadcrumb{display:flex;flex-wrap:wrap;gap:4px;align-items:center;font-size:13px;color:#6b7280}
.breadcrumb .sep{margin:0 2px;color:#d1d5db}
.breadcrumb .current{color:#111827;font-weight:500}
.breadcrumb a{color:#0ea5e9}
.breadcrumb a:hover{text-decoration:underline}
.file-list{}
.file-row{display:flex;align-items:center;padding:4px 12px;border-top:1px solid #f3f4f6;cursor:pointer;user-select:none;gap:10px;min-height:36px;color:#374151}
.file-row:first-child{border-top:none}
.file-row:hover:not(.selected){background:#f9fafb}
.file-row.selected{background:#0ea5e9;color:#fff}
.row-icon{font-size:15px;line-height:1;flex:0 0 auto}
.row-name{flex:1;font-size:13px;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;display:flex;align-items:center;gap:8px}
.row-size{font-size:12px;color:#9ca3af;white-space:nowrap}
.file-row.selected .row-size{color:rgba(255,255,255,0.75)}
.download-btn{display:inline-flex;align-items:center;justify-content:center;width:26px;height:26px;border-radius:6px;background:#eff6ff;color:#0ea5e9;flex:0 0 auto;transition:background 0.15s}
.download-btn:hover{background:#dbeafe}
.file-row.selected .download-btn{background:rgba(255,255,255,0.25);color:#fff}
.file-row.selected .download-btn:hover{background:rgba(255,255,255,0.4)}
.footer-hint{font-family:'JetBrains Mono',monospace;font-size:11px;color:#9ca3af;text-align:center;letter-spacing:0.04em;margin-top:16px;line-height:1.8}
.footer-hint a{color:#0ea5e9}
.footer-hint a:hover{text-decoration:underline}
.error{background:#fef2f2;border:1px solid #fecaca;color:#dc2626;padding:12px 16px;margin:12px;border-radius:6px}
.loading,.empty{color:#9ca3af;padding:40px 16px;text-align:center}
@media(max-width:600px){
body{padding:16px 12px}
.file-row{padding:4px 10px}
}
`;
