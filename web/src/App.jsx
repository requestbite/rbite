import { h } from "preact";
import { useState, useEffect } from "preact/hooks";

function formatSize(bytes) {
  if (bytes < 1024) return bytes + " B";
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + " KB";
  if (bytes < 1024 * 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(1) + " MB";
  return (bytes / (1024 * 1024 * 1024)).toFixed(1) + " GB";
}

function formatDate(iso) {
  const d = new Date(iso);
  return d.toLocaleDateString() + " " + d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

export default function App() {
  const [path, setPath] = useState("");
  const [entries, setEntries] = useState([]);
  const [error, setError] = useState(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    load(path);
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

  function breadcrumbs() {
    const parts = path ? path.split("/") : [];
    return [
      { label: "~", path: "" },
      ...parts.map((p, i) => ({ label: p, path: parts.slice(0, i + 1).join("/") })),
    ];
  }

  return (
    <div class="container">
      <header>
        <h1>
          <span class="logo">⚡</span> rbite file share
        </h1>
        <nav class="breadcrumb">
          {breadcrumbs().map((crumb, i, arr) => (
            <span key={crumb.path}>
              {i > 0 && <span class="sep">/</span>}
              {i < arr.length - 1 ? (
                <a href="#" onClick={(e) => { e.preventDefault(); setPath(crumb.path); }}>
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
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Size</th>
                <th>Modified</th>
              </tr>
            </thead>
            <tbody>
              {entries.map((e) => (
                <tr key={e.name}>
                  <td class="name-cell">
                    {e.isDir ? (
                      <a href="#" class="dir" onClick={(ev) => { ev.preventDefault(); navigate(e.name); }}>
                        📁 {e.name}
                      </a>
                    ) : (
                      <a href={"/api/download?path=" + encodeURIComponent(path ? path + "/" + e.name : e.name)} class="file">
                        📄 {e.name}
                      </a>
                    )}
                  </td>
                  <td class="size-cell">{e.isDir ? "—" : formatSize(e.size)}</td>
                  <td class="date-cell">{formatDate(e.modTime)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </main>
    </div>
  );
}
