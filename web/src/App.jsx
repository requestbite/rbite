import { h } from "preact";
import { useState, useEffect, useRef } from "preact/hooks";
import { HardDriveDownload, Copy } from "lucide-preact";

function Logo() {
  return (
    <svg width="60" height="60" viewBox="0 0 41 41" xmlns="http://www.w3.org/2000/svg">
      <g transform="translate(-0.26689062,-0.31415232)">
        <path fill="#ff8080" d="m 24.530376,22.876419 c -1.721217,0.06834 -3.694272,0.507218 -4.735954,1.977396 0.763356,1.283375 2.464789,2.23238 2.856533,3.684057 -0.851343,0.114355 -0.782817,-1.079997 -1.463038,-1.386495 -1.873994,-2.024664 -4.521105,-3.391849 -7.331809,-3.157892 -2.161673,0.153401 -4.7809416,0.02045 -6.4325488,1.655047 -0.282181,1.254132 1.3179097,1.985558 2.1261037,2.674581 2.8736871,1.932916 6.2256951,3.522634 9.7803961,3.132706 4.523788,-0.319788 9.187529,-1.685255 12.614786,-4.775506 0.340423,-1.01084 -1.221392,-1.547733 -1.860614,-2.175087 -1.598296,-1.187011 -3.578718,-1.700391 -5.553855,-1.628807 z"/>
        <path fill="#666666" d="m 14.047245,12.988567 c -3.35175,0.545872 -7.0797641,2.26776 -8.0797248,5.808843 -0.3412758,1.39805 -0.6049919,3.080759 0.06342,4.392178 1.1879636,0.455049 2.2884992,-0.819059 3.5230995,-0.686695 1.7665033,8.02e-4 3.6617623,-0.489089 5.3403203,0.06325 1.164036,0.316621 2.3003,1.375219 3.489512,1.222578 2.747808,-2.959656 7.725063,-3.302783 10.954627,-0.953044 1.263938,0.649791 1.98861,2.159805 3.293037,2.632156 1.178887,-0.810637 1.900644,-2.234452 2.542052,-3.49642 0.396612,-1.582392 0.656343,-3.375712 0.225689,-4.956824 -1.139738,-0.773994 -2.463189,0.619543 -3.709861,0.465811 -3.899431,0.110548 -7.883893,0.444595 -11.5932,-1.016036 -2.365215,-0.302309 -3.801468,-2.493312 -5.861331,-3.464926 l -0.117657,-0.01314 z"/>
        <path fill="#f9f9f9" d="m 15.578375,12.9461 6.959626,3.488202 5.527721,0.527398 7.607728,-1.160958 -1.231339,-3.791212 -3.898307,-2.232069 -4.442286,0.52275 -4.777421,1.70927 z"/>
        <path fill="#000000" d="M 16.446629,32.930396 C 14.332478,32.718519 12.320308,31.882143 10.618161,30.630452 9.0055901,29.669271 7.4514818,28.61069 6.206028,27.184183 c -1.93523,-1.66516 -2.4676132,-4.345627 -2.2111746,-6.78475 0.4280194,-1.618538 0.839694,-3.313274 1.7509985,-4.728407 0.5951707,-1.416824 2.2419546,-2.45032 3.7250703,-2.857242 1.5935968,-0.776753 3.3513018,-1.004989 5.0958698,-1.202905 2.055722,-0.1718 4.132712,0.404652 6.154157,-0.152978 1.885655,-0.06367 3.250862,-1.4635178 4.948878,-2.0859126 1.689871,-0.844429 3.671979,-0.8690873 5.49065,-0.5412935 1.805357,0.4556646 3.689963,1.3575761 4.463971,3.1727531 0.853394,1.131133 1.440356,3.142612 1.684898,3.974251 0.07201,1.680323 0.611682,3.389029 0.0041,5.140173 -0.818733,1.284557 -1.42202,2.696352 -2.482167,3.847676 -1.216477,1.824124 -2.986529,3.190955 -4.722915,4.492779 -2.146842,1.275057 -4.464138,2.485596 -6.954426,2.969941 -1.861972,0.353947 -3.831235,0.02857 -5.665613,0.561925 -0.348178,0.01311 -0.696325,-0.02097 -1.041576,-0.0598 z M 21.875101,30.9267 c 1.983117,-0.419768 3.98354,-0.808247 5.79491,-1.784079 1.523675,-0.750107 3.039034,-1.586189 4.210971,-2.846896 -1.144069,-0.904198 -2.212451,-1.965429 -3.61393,-2.507265 -1.538719,-0.80697 -3.363984,-0.55472 -5.032382,-0.528783 -0.980727,0.202887 -3.3558,0.985351 -3.040944,1.857527 1.102454,0.984418 2.348186,1.934253 2.854073,3.35421 -1.004812,1.291073 -1.717119,-1.340091 -2.753838,-1.807884 -1.229309,-1.160573 -2.740441,-2.039247 -4.464743,-2.299133 -2.607168,-0.204793 -5.377261,-0.208486 -7.7575234,0.998742 -1.5275336,0.734678 0.9130305,2.07741 1.6349057,2.776236 2.5806347,1.801009 5.6361757,3.188203 8.8512847,3.019229 1.10414,-0.02296 2.242862,0.07679 3.317216,-0.231904 z m 10.844684,-5.592243 c 0.989588,-1.530863 2.531204,-2.981634 2.486101,-4.935905 0.302167,-1.326026 0.530416,-4.905105 -1.510806,-3.016951 -3.289762,0.813286 -6.7624,0.84083 -10.109347,0.360019 -1.75704,-0.465583 -3.480458,-0.913552 -5.208936,-1.407093 -1.504778,-0.862071 -2.648877,-2.455108 -4.280513,-2.980014 -2.460853,0.262473 -5.073356,1.175619 -6.6250127,3.197668 -1.1476099,1.493154 -1.8230338,3.367257 -1.5488607,5.267762 -0.1554016,2.764295 2.4134089,-0.0034 3.8484234,0.26603 1.72057,-0.161321 3.548361,-0.278196 5.21439,0.232439 1.170315,-0.07102 2.458193,1.833383 3.405395,0.995618 1.563227,-1.698931 3.94031,-2.265903 6.16794,-2.365107 2.249079,-0.0308 4.423135,0.954848 6.031311,2.461394 0.583909,0.612239 1.609173,1.599098 2.129915,1.92414 z m -3.156092,-9.497134 c 0.49708,-1.293124 0.795088,-2.619402 0.658209,-3.972184 0.237209,-1.393927 -2.281402,-0.579003 -3.100967,-0.532975 0.118111,1.429124 -1.081353,3.651452 -0.143741,4.596809 0.843649,-0.20135 1.911634,0.513865 2.586499,-0.09164 z m 2.589954,8.6e-4 c 1.247384,-0.309295 3.518873,-0.54603 2.069555,-2.220655 -0.649173,-1.109327 -3.733744,-3.246142 -2.907982,-0.473943 0.126636,1.034398 -1.423699,3.713013 0.838427,2.694598 z M 25.58501,15.28805 c -0.08808,-1.046118 0.863268,-3.168443 -0.03083,-3.590473 -1.304398,0.777316 -3.688743,0.770091 -3.569333,2.760021 -0.44851,1.290461 2.104407,1.232513 3.001606,1.312445 0.455129,0.0061 0.690522,0.09044 0.59854,-0.481993 z m -4.638771,-0.770211 c 0.517635,-1.43808 -0.481284,-1.337737 -1.613824,-1.122639 -2.6893,-0.0026 1.302793,1.57612 1.613824,1.122639 z"/>
      </g>
    </svg>
  );
}

function formatSize(bytes) {
  if (bytes < 1024) return bytes + " B";
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + " KB";
  if (bytes < 1024 * 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(1) + " MB";
  return (bytes / (1024 * 1024 * 1024)).toFixed(1) + " GB";
}

export default function App() {
  const [path, setPath] = useState(decodeURIComponent(window.location.hash.replace(/^#\/?/, "")));
  const [entries, setEntries] = useState([]);
  const [error, setError] = useState(null);
  const [loading, setLoading] = useState(false);
  const [selectedName, setSelectedName] = useState(null);
  const [toastVisible, setToastVisible] = useState(false);
  const clickTimerRef = useRef(null);
  const lastClickRef = useRef(null);
  const toastTimerRef = useRef(null);

  function showToast() {
    if (toastTimerRef.current) clearTimeout(toastTimerRef.current);
    setToastVisible(true);
    toastTimerRef.current = setTimeout(() => {
      setToastVisible(false);
      toastTimerRef.current = null;
    }, 3000);
  }

  useEffect(() => {
    const hash = "#/" + path;
    if (window.location.hash !== hash) history.pushState(null, "", hash);
    load(path);
    setSelectedName(null);
  }, [path]);

  useEffect(() => {
    function onPop() {
      setPath(decodeURIComponent(window.location.hash.replace(/^#\/?/, "")));
    }
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

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
    <div class="max-w-[880px] mx-auto">
      <div class="flex flex-col items-center mb-5 gap-2.5">
        <Logo />
        <p class="font-jetbrains text-sm text-gray-400 tracking-[0.04em] text-center">RequestBite Tunnel</p>
      </div>
      <div class="bg-white rounded-lg overflow-hidden [outline:1px_solid_#d1d5db] [outline-offset:-1px]">
        <header class="bg-white py-2.5 px-4 border-b border-gray-200">
          <nav class="flex flex-wrap gap-1 items-center text-[13px] text-gray-500">
            {breadcrumbs().map((crumb, i, arr) => (
              <span key={crumb.path}>
                {i > 0 && <span class="mx-0.5 text-gray-300">/</span>}
                {i < arr.length - 1 ? (
                  <a
                    href="#"
                    class="text-sky-500 hover:underline"
                    onClick={(e) => { e.preventDefault(); navigateBreadcrumb(crumb.path); }}
                  >
                    {crumb.label}
                  </a>
                ) : (
                  <span class="text-gray-900 font-medium">{crumb.label}</span>
                )}
              </span>
            ))}
          </nav>
        </header>

        <main>
          {error && (
            <div class="bg-red-50 border border-red-200 text-red-600 px-4 py-3 m-3 rounded-md">
              {error}
            </div>
          )}
          {loading && <div class="text-gray-400 py-10 px-4 text-center">Loading…</div>}
          {!loading && !error && entries.length === 0 && (
            <div class="text-gray-400 py-10 px-4 text-center">Empty directory</div>
          )}
          {!loading && !error && entries.length > 0 && (
            <div>
              {entries.map((e) => {
                const selected = selectedName === e.name;
                return (
                  <div
                    key={e.name}
                    class={`flex items-center px-3 max-[600px]:px-[10px] py-1 border-t border-gray-100 first:border-t-0 cursor-pointer select-none gap-2.5 min-h-[36px] ${
                      selected ? "bg-sky-500 text-white" : "text-gray-700 hover:bg-gray-50"
                    }`}
                    onClick={() => handleRowClick(e)}
                  >
                    <span class="text-[15px] leading-none shrink-0">{e.isDir ? "📁" : "📄"}</span>
                    <span class="flex-1 text-[13px] min-w-0 overflow-hidden text-ellipsis whitespace-nowrap flex items-center gap-2">
                      {e.name}
                      {!e.isDir && (
                        <span class={`text-xs whitespace-nowrap ${selected ? "text-white/75" : "text-gray-400"}`}>
                          {formatSize(e.size)}
                        </span>
                      )}
                    </span>
                    {e.isDir && (
                      <button
                        class={`inline-flex items-center justify-center w-[26px] h-[26px] rounded-md shrink-0 transition-colors duration-150 ${
                          selected
                            ? "bg-white/25 text-white hover:bg-white/40"
                            : "bg-blue-50 text-sky-500 hover:bg-blue-100"
                        }`}
                        title="Copy link"
                        onClick={(ev) => {
                          ev.stopPropagation();
                          const dirPath = path ? path + "/" + e.name : e.name;
                          const url = window.location.origin + window.location.pathname + "#/" + dirPath;
                          navigator.clipboard.writeText(url);
                          showToast();
                        }}
                      >
                        <Copy size={15} />
                      </button>
                    )}
                    {!e.isDir && (
                      <a
                        class={`inline-flex items-center justify-center w-[26px] h-[26px] rounded-md shrink-0 transition-colors duration-150 ${
                          selected
                            ? "bg-white/25 text-white hover:bg-white/40"
                            : "bg-blue-50 text-sky-500 hover:bg-blue-100"
                        }`}
                        href={"/api/download?path=" + encodeURIComponent(path ? path + "/" + e.name : e.name)}
                        title="Download"
                        onClick={(ev) => ev.stopPropagation()}
                      >
                        <HardDriveDownload size={15} />
                      </a>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </main>
      </div>
      <p class="font-jetbrains text-[11px] text-gray-400 text-center tracking-[0.04em] mt-4 leading-[1.8]">
        Easily share your localhost through a RequestBite Tunnel.<br />
        Read more at <a href="https://requestbite.com/tunnel" class="text-sky-500 hover:underline" target="_blank" rel="noopener noreferrer">requestbite.com/tunnel</a>.
      </p>
      <p class="font-jetbrains text-[11px] text-gray-400 text-center tracking-[0.04em] mt-4 leading-[1.8]">rbite {"{{VERSION}}"}</p>
      <div
        class={`fixed bottom-5 right-5 transition-all duration-300 ${toastVisible ? "opacity-100 translate-y-0" : "opacity-0 translate-y-2 pointer-events-none"}`}
        style={{ zIndex: 99999 }}
      >
        <div class="flex items-start gap-3 rounded-lg border-2 border-green-800 bg-green-100 p-4 shadow-lg w-80">
          <svg class="size-5 shrink-0 text-green-800 mt-0.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" aria-hidden="true">
            <path stroke-linecap="round" stroke-linejoin="round" d="M9 12.75 11.25 15 15 9.75M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" />
          </svg>
          <p class="text-sm font-medium text-green-800">Path copied to clipboard.</p>
        </div>
      </div>
    </div>
  );
}
