// Auth modal and passphrase protection for the file browser.
// Injected as a plain-JS <script> block by build.js so it runs after the
// Preact app but without being bundled through esbuild.
(function () {
  var authRequired = window.__AUTH_REQUIRED__ === true;
  if (!authRequired) return;

  var passphrase = localStorage.getItem("rbite-passphrase") || "";

  // Safe base64 that handles Unicode passphrases
  function b64(str) {
    try {
      return btoa(unescape(encodeURIComponent(str)));
    } catch (e) {
      return btoa(str);
    }
  }

  // ── Error toast (matches slingshot Toast error style) ────────────────────
  var toastEl = null;
  var toastTimer = null;

  function showToast(msg) {
    if (!toastEl) {
      toastEl = document.createElement("div");
      toastEl.style.cssText =
        "position:fixed;bottom:1.25rem;right:1.25rem;z-index:99999;" +
        "width:24rem;max-width:calc(100vw - 2.5rem);" +
        "transition:opacity .3s ease-out,transform .3s ease-out;" +
        "opacity:0;transform:translateY(.5rem);pointer-events:none;";
      toastEl.innerHTML =
        '<div style="overflow:hidden;border-radius:.5rem;border:2px solid #dc2626;background:#fef2f2;box-shadow:0 10px 15px -3px rgba(0,0,0,.1),0 4px 6px -4px rgba(0,0,0,.1);">' +
        '<div style="padding:1rem;">' +
        '<div style="display:flex;align-items:flex-start;">' +
        '<svg style="flex-shrink:0;width:1.5rem;height:1.5rem;color:#dc2626;" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">' +
        '<path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z"/>' +
        "</svg>" +
        '<div style="margin-left:.75rem;flex:1;min-width:0;padding-top:.125rem;">' +
        '<p id="rbite-toast-msg" style="margin:0;font-size:.875rem;font-weight:500;color:#4b5563;"></p>' +
        "</div>" +
        '<div style="margin-left:1rem;flex-shrink:0;">' +
        '<button id="rbite-toast-close" type="button" style="display:inline-flex;background:none;border:none;padding:0;color:#4b5563;cursor:pointer;">' +
        '<svg style="width:1.25rem;height:1.25rem;" viewBox="0 0 20 20" fill="currentColor">' +
        '<path d="M6.28 5.22a.75.75 0 0 0-1.06 1.06L8.94 10l-3.72 3.72a.75.75 0 1 0 1.06 1.06L10 11.06l3.72 3.72a.75.75 0 1 0 1.06-1.06L11.06 10l3.72-3.72a.75.75 0 0 0-1.06-1.06L10 8.94 6.28 5.22Z"/>' +
        "</svg>" +
        "</button>" +
        "</div>" +
        "</div>" +
        "</div>" +
        "</div>";
      document.body.appendChild(toastEl);
      document.getElementById("rbite-toast-close").onclick = hideToast;
    }
    clearTimeout(toastTimer);
    document.getElementById("rbite-toast-msg").textContent = msg;
    toastEl.style.opacity = "1";
    toastEl.style.transform = "translateY(0)";
    toastEl.style.pointerEvents = "auto";
    toastTimer = setTimeout(hideToast, 4000);
  }

  function hideToast() {
    if (!toastEl) return;
    clearTimeout(toastTimer);
    toastEl.style.opacity = "0";
    toastEl.style.transform = "translateY(.5rem)";
    toastEl.style.pointerEvents = "none";
  }

  // ── Modal (matches slingshot Modal + AddCollectionModal styling) ──────────
  // Outer layer: fully opaque body-colour background so no file browser
  // error messages bleed through the backdrop on first load.
  var modal = document.createElement("div");
  modal.id = "auth-modal";
  modal.style.cssText =
    "display:none;position:fixed;top:0;left:0;right:0;bottom:0;" +
    "z-index:1000;background-color:rgb(249,250,251);";

  // Gray semi-transparent overlay + centering container
  var inner = document.createElement("div");
  inner.style.cssText =
    "position:absolute;top:0;left:0;right:0;bottom:0;" +
    "background-color:rgba(107,114,128,.75);" +
    "display:flex;align-items:center;justify-content:center;" +
    "padding:1rem;overflow-y:auto;";

  // Dialog card (matches slingshot rounded-lg bg-white shadow-xl)
  var card = document.createElement("div");
  card.style.cssText =
    "position:relative;background:white;border-radius:.5rem;padding:1.5rem;" +
    "max-width:32rem;width:100%;" +
    "box-shadow:0 20px 25px -5px rgba(0,0,0,.1),0 10px 10px -5px rgba(0,0,0,.04);" +
    "text-align:left;";
  card.onclick = function (e) { e.stopPropagation(); };

  // Title (matches slingshot text-base font-semibold text-gray-900)
  var titleEl = document.createElement("h3");
  titleEl.textContent = "Protected Access";
  titleEl.style.cssText = "margin:0;font-size:1rem;font-weight:600;color:#111827;";

  // Description (matches AddCollectionModal text-sm text-gray-500)
  var descEl = document.createElement("div");
  descEl.textContent = "Please enter the required passphrase to access files.";
  descEl.style.cssText = "margin-top:.5rem;font-size:.875rem;color:#6b7280;";

  // Form — browser submits on Enter automatically via type="submit"
  var form = document.createElement("form");
  form.onsubmit = function (e) { e.preventDefault(); handleSubmit(); };

  // Input wrapper (matches AddCollectionModal mt-6)
  var inputWrap = document.createElement("div");
  inputWrap.style.cssText = "margin-top:1.5rem;";

  var inputEl = document.createElement("input");
  inputEl.type = "password";
  inputEl.id = "auth-passphrase-input";
  inputEl.placeholder = "Passphrase";
  inputEl.autocomplete = "current-password";
  inputEl.style.cssText =
    "width:100%;box-sizing:border-box;padding:.5rem .75rem;" +
    "border:1px solid #d1d5db;border-radius:.375rem;" +
    "font-size:.875rem;line-height:1.5;color:#111827;background:white;" +
    "outline:none;font-family:inherit;" +
    "transition:border-color .15s,box-shadow .15s;";
  inputEl.addEventListener("focus", function () {
    inputEl.style.borderColor = "#38bdf8";
    inputEl.style.boxShadow = "0 0 0 2px rgba(56,189,248,.4)";
  });
  inputEl.addEventListener("blur", function () {
    inputEl.style.borderColor = "#d1d5db";
    inputEl.style.boxShadow = "none";
  });
  inputWrap.appendChild(inputEl);

  // Buttons (matches AddCollectionModal sm:flex sm:flex-row-reverse)
  var btnRow = document.createElement("div");
  btnRow.style.cssText = "margin-top:1.25rem;display:flex;flex-direction:row-reverse;gap:.75rem;";

  // Primary button (matches slingshot Button primary variant, sky-500)
  var okBtn = document.createElement("button");
  okBtn.type = "submit";
  okBtn.textContent = "Unlock";
  okBtn.style.cssText =
    "display:inline-flex;align-items:center;justify-content:center;" +
    "padding:.5rem 1rem;background-color:#0ea5e9;color:white;" +
    "border:none;border-radius:.375rem;font-size:.875rem;font-weight:500;" +
    "cursor:pointer;transition:background-color .15s;" +
    "font-family:inherit;line-height:1.5;";
  okBtn.addEventListener("mouseover", function () {
    if (!okBtn.disabled) okBtn.style.backgroundColor = "#0284c7";
  });
  okBtn.addEventListener("mouseout", function () {
    if (!okBtn.disabled) okBtn.style.backgroundColor = "#0ea5e9";
  });
  btnRow.appendChild(okBtn);

  form.appendChild(inputWrap);
  form.appendChild(btnRow);
  card.appendChild(titleEl);
  card.appendChild(descEl);
  card.appendChild(form);
  inner.appendChild(card);
  modal.appendChild(inner);
  document.body.appendChild(modal);

  // ── Show / hide modal ─────────────────────────────────────────────────────
  function showModal() {
    modal.style.display = "block";
    setTimeout(function () { inputEl.value = ""; inputEl.focus(); }, 50);
  }

  function hideModal() {
    modal.style.display = "none";
  }

  // ── Submit: verify via XHR (bypasses our fetch interceptor), reload on OK ─
  function handleSubmit() {
    var entered = inputEl.value;
    if (!entered) return;

    okBtn.disabled = true;
    okBtn.style.opacity = ".6";
    okBtn.style.cursor = "not-allowed";
    okBtn.textContent = "Checking…";

    var xhr = new XMLHttpRequest();
    xhr.open("GET", "/api/ls?path=", true);
    xhr.setRequestHeader("Authorization", "Basic " + b64(":" + entered));
    xhr.onload = function () {
      if (xhr.status === 200) {
        passphrase = entered;
        localStorage.setItem("rbite-passphrase", entered);
        window.location.reload(); // modal stays visible until reload removes it
      } else if (xhr.status === 401) {
        showToast("Incorrect passphrase. Please try again.");
        inputEl.value = "";
        inputEl.focus();
        resetBtn();
      } else {
        showToast("Something went wrong. Please try again.");
        resetBtn();
      }
    };
    xhr.onerror = function () {
      showToast("Connection error. Please try again.");
      resetBtn();
    };
    xhr.send();
  }

  function resetBtn() {
    okBtn.disabled = false;
    okBtn.style.opacity = "1";
    okBtn.style.cursor = "pointer";
    okBtn.textContent = "Unlock";
  }

  // Show immediately when no passphrase is stored
  if (!passphrase) showModal();

  // ── Fetch interceptor: attach Basic Auth to every /api/ request ───────────
  var origFetch = window.fetch;
  window.fetch = function (url, options) {
    if (typeof url === "string" && url.includes("/api/")) {
      var pw = passphrase;
      if (pw) {
        var opts = options ? Object.assign({}, options) : {};
        opts.headers = Object.assign({}, opts.headers || {}, {
          Authorization: "Basic " + b64(":" + pw),
        });
        return origFetch.call(window, url, opts).then(function (res) {
          if (res.status === 401) {
            passphrase = "";
            localStorage.removeItem("rbite-passphrase");
            showModal();
          }
          return res;
        });
      }
    }
    return origFetch.call(window, url, options);
  };
})();
