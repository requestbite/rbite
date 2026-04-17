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
        --views-list
        --views-add
        --views-tail
        --views-open
        -e --ephemeral
        -r --resume
        --tunnel-server
        --no-upgrade-check
        -h --help
        -v --version
    )

    case "$prev" in
        -e|--ephemeral)
            # Expects a port number; offer nothing to let the user type freely
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
