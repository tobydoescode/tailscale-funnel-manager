#!/usr/bin/env bash
#
# Dynamically determines if a Docker build is needed by checking whether any
# changed file is one of the known image inputs.
#
# Usage: ./scripts/docker-build-needed.sh <base-ref> [head-ref]
# Output: build-needed=true|false to $GITHUB_OUTPUT (or stdout if unset)
#
set -euo pipefail

BASE_REF="${1:-HEAD~1}"
HEAD_REF="${2:-HEAD}"

emit() {
    if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
        echo "build-needed=$1" >> "$GITHUB_OUTPUT"
    else
        echo "build-needed=$1"
    fi
}

# New branch (null SHA) — can't diff, assume build needed
if [[ "$BASE_REF" =~ ^0+$ ]]; then
    echo "New branch detected, cannot diff — assuming build needed" >&2
    emit true
    exit 0
fi

# Changed files between base and head
changed=$(git diff --name-only "$BASE_REF" "$HEAD_REF" 2>/dev/null) || {
    echo "git diff failed — assuming build needed" >&2
    emit true
    exit 0
}

if [[ -z "$changed" ]]; then
    echo "No files changed" >&2
    emit false
    exit 0
fi

is_relevant() {
    local file="$1"

    case "$file" in
        Dockerfile|.dockerignore|go.mod|go.sum|main.go)
            return 0
            ;;
        internal/*)
            return 0
            ;;
        *)
            return 1
            ;;
    esac
}

build_needed=false
while IFS= read -r file; do
    [[ -z "$file" ]] && continue
    if is_relevant "$file"; then
        echo "Build-relevant change: $file" >&2
        build_needed=true
        break
    fi
done <<< "$changed"

if ! $build_needed; then
    echo "No build-relevant files changed" >&2
fi

emit "$build_needed"
