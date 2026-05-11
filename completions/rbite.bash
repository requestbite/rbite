# bash completion for rbite
# Source this file or place it in:
#   Linux:  ~/.local/share/bash-completion/completions/rbite
#   macOS:  $(brew --prefix)/etc/bash_completion.d/rbite

_rbite() {
    local cur prev words cword
    _init_completion || return

    local all_flags=(
        --login
        --switch-accounts
        --whoami
        --views-list
        --views-add
        --views-tail
        --views-open
        -f --files
        --files-write
        -p --passphrase
        -w --web-server
        --spa
        -e --ephemeral
        -t --tunnels
        --tunnels-list
        -r --resume
        --show-qr
        --localhost-rewrite
        --tunnel-server
        --no-upgrade-check
        --uninstall
        -h --help
        -v --version
    )

    case "$prev" in
        -f|--files|--files-write|-w|--web-server)
            # Expects a directory path
            _filedir -d
            return 0
            ;;
        --spa)
            # Optional index file path
            _filedir
            return 0
            ;;
        -p|--passphrase)
            # Expects a passphrase string; offer nothing
            COMPREPLY=()
            return 0
            ;;
        -e|--ephemeral)
            # Expects a port number; offer nothing to let the user type freely
            COMPREPLY=()
            return 0
            ;;
        -t|--tunnels)
            # Expects a tunnel name; offer nothing
            COMPREPLY=()
            return 0
            ;;
        --tunnel-server)
            # Expects a URL; offer nothing
            COMPREPLY=()
            return 0
            ;;
        --views-add)
            # Optional view name; offer nothing
            COMPREPLY=()
            return 0
            ;;
        --views-tail|--views-open)
            # Optional view ID; offer nothing
            COMPREPLY=()
            return 0
            ;;
    esac

    # Complete flags
    if [[ "$cur" == -* ]]; then
        COMPREPLY=( $(compgen -W "${all_flags[*]}" -- "$cur") )
        return 0
    fi
}

complete -F _rbite rbite
