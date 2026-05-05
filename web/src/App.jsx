import { h } from "preact";
import { useState, useEffect, useRef } from "preact/hooks";

function formatSize(bytes) {
  if (bytes < 1024) return bytes + " B";
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + " KB";
  if (bytes < 1024 * 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(1) + " MB";
  return (bytes / (1024 * 1024 * 1024)).toFixed(1) + " GB";
}

function HardDriveDownloadIcon() {
  return (
    <svg xmlns="http://www.w3.org/2000/svg" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
      <path d="M12 2v8"/>
      <path d="m16 6-4 4-4-4"/>
      <rect width="20" height="8" x="2" y="14" rx="2"/>
      <path d="M6 18h.01"/>
      <path d="M10 18h.01"/>
    </svg>
  );
}

export default function App() {
  const [path, setPath] = useState("");
  const [entries, setEntries] = useState([]);
  const [error, setError] = useState(null);
  const [loading, setLoading] = useState(false);
  const [selectedName, setSelectedName] = useState(null);
  const clickTimerRef = useRef(null);
  const lastClickRef = useRef(null);

  useEffect(() => {
    load(path);
    setSelectedName(null);
  }, [path]);

  async function load(p) {
    setLoading(true);
    setError(null);
    try {
      const res = await fetch("/api/ls?path=" + encodeURIComponent(p));
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      setEntries(data.entries || []);
    } catch (e) {
      setError(e.message);
    } finally {
      setLoading(false);
    }
  }

  function navigate(name) {
    setPath(path ? path + "/" + name : name);
  }

  function handleRowClick(entry) {
    if (clickTimerRef.current && lastClickRef.current === entry.name) {
      clearTimeout(clickTimerRef.current);
      clickTimerRef.current = null;
      lastClickRef.current = null;
      if (entry.isDir) navigate(entry.name);
    } else {
      if (clickTimerRef.current) clearTimeout(clickTimerRef.current);
      setSelectedName(entry.name);
      lastClickRef.current = entry.name;
      clickTimerRef.current = setTimeout(() => {
        clickTimerRef.current = null;
        lastClickRef.current = null;
      }, 250);
    }
  }

  function navigateBreadcrumb(p) {
    setSelectedName(null);
    setPath(p);
  }

  function breadcrumbs() {
    const parts = path ? path.split("/") : [];
    return [
      { label: "~", path: "" },
      ...parts.map((p, i) => ({ label: p, path: parts.slice(0, i + 1).join("/") })),
    ];
  }

  return (
    <div class="page">
      <p class="site-title">RequestBite Tunnel</p>
      <div class="browser-card">
        <header>
          <nav class="breadcrumb">
            {breadcrumbs().map((crumb, i, arr) => (
              <span key={crumb.path}>
                {i > 0 && <span class="sep">/</span>}
                {i < arr.length - 1 ? (
                  <a href="#" onClick={(e) => { e.preventDefault(); navigateBreadcrumb(crumb.path); }}>
                    {crumb.label}
                  </a>
                ) : (
                  <span class="current">{crumb.label}</span>
                )}
              </span>
            ))}
          </nav>
        </header>

        <main>
          {error && <div class="error">{error}</div>}
          {loading && <div class="loading">Loading…</div>}
          {!loading && !error && entries.length === 0 && (
            <div class="empty">Empty directory</div>
          )}
          {!loading && !error && entries.length > 0 && (
            <div class="file-list">
              {entries.map((e) => (
                <div
                  key={e.name}
                  class={`file-row${selectedName === e.name ? " selected" : ""}`}
                  onClick={() => handleRowClick(e)}
                >
                  <span class="row-icon">{e.isDir ? "📁" : "📄"}</span>
                  <span class="row-name">
                    {e.name}
                    {!e.isDir && (
                      <span class="row-size">{formatSize(e.size)}</span>
                    )}
                  </span>
                  {!e.isDir && (
                    <a
                      class="download-btn"
                      href={"/api/download?path=" + encodeURIComponent(path ? path + "/" + e.name : e.name)}
                      title="Download"
                      onClick={(ev) => ev.stopPropagation()}
                    >
                      <HardDriveDownloadIcon />
                    </a>
                  )}
                </div>
              ))}
            </div>
          )}
        </main>
      </div>
    </div>
  );
}
