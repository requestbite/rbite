# fish completion for rbite
# Place this file at:
#   ~/.config/fish/completions/rbite.fish

# Disable file completion for rbite by default
complete -c rbite -f

# ── Account management ────────────────────────────────────────────────────────
complete -c rbite -l login \
    -d 'Log in via browser'

complete -c rbite -l switch-accounts \
    -d 'Switch the active account'

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
complete -c rbite -s e -l ephemeral -r \
    -d 'Port to expose via ephemeral tunnel'

complete -c rbite -s r -l resume \
    -d 'Resume the last tunnel session if not expired'

complete -c rbite -l show-qr \
    -d 'Print a QR code of the tunnel URL (use with -e or -r)'

complete -c rbite -l tunnel-server -r \
    -d 'Tunnel server URL (default: https://api.t.rbite.dev)'

# ── Other ─────────────────────────────────────────────────────────────────────
complete -c rbite -l no-upgrade-check \
    -d 'Disable automatic upgrade check on startup'

complete -c rbite -s h -l help \
    -d 'Show help information'

complete -c rbite -s v -l version \
    -d 'Show version and build information'
