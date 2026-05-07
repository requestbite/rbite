# fish completion for rbite
# Place this file at:
#   ~/.config/fish/completions/rbite.fish

# Disable file completion for rbite by default (re-enabled per flag below)
complete -c rbite -f

# ── Account management ────────────────────────────────────────────────────────
complete -c rbite -l login \
    -d 'Log in via browser'

complete -c rbite -l switch-accounts \
    -d 'Switch the active account'

complete -c rbite -l whoami \
    -d 'Show logged-in user and account details'

# ── Request views ─────────────────────────────────────────────────────────────
complete -c rbite -l views-list \
    -d 'List all active inspector views for the current account'

complete -c rbite -l views-add \
    -d 'Create a new inspector view (name is optional)'

complete -c rbite -l views-tail \
    -d 'Stream live requests for a view (prompts for selection if no ID given)'

complete -c rbite -l views-open \
    -d 'Open a view capture URL in the browser (prompts for selection if no ID given)'

# ── Tunnel management ─────────────────────────────────────────────────────────
complete -c rbite -s f -l files -r -F \
    -d 'Share a local directory via ephemeral tunnel (read-only)'

complete -c rbite -l files-write -r -F \
    -d 'Share a local directory via ephemeral tunnel with upload support (read/write); short form: -fw'

complete -c rbite -s e -l ephemeral -r \
    -d 'Port to expose via ephemeral tunnel'

complete -c rbite -s t -l tunnels -r \
    -d 'Connect a permanent tunnel by name'

complete -c rbite -l tunnels-list \
    -d 'List tunnels for the current account'

complete -c rbite -s r -l resume \
    -d 'Resume the last tunnel session if not expired'

complete -c rbite -l show-qr \
    -d 'Print a QR code of the tunnel URL (use with -e or -r)'

complete -c rbite -l localhost-rewrite \
    -d 'Rewrite localhost URLs in responses to the tunnel public URL (use with -t)'

complete -c rbite -l tunnel-server -r \
    -d 'Tunnel server URL (default: https://api.t.rbite.dev)'

# ── Other ─────────────────────────────────────────────────────────────────────
complete -c rbite -l no-upgrade-check \
    -d 'Disable automatic upgrade check on startup'

complete -c rbite -l uninstall \
    -d 'Uninstall rbite (interactive)'

complete -c rbite -s h -l help \
    -d 'Show help information'

complete -c rbite -s v -l version \
    -d 'Show version and build information'
