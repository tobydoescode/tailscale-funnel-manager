#!/usr/bin/env bash
#
# Dynamically determines if a Docker build is needed by parsing the Dockerfile
# for COPY/ADD instructions, applying .dockerignore exclusions, and checking
# whether any changed files are build-relevant.
#
# Usage: ./scripts/docker-build-needed.sh <base-ref> [head-ref]
# Output: build-needed=true|false to $GITHUB_OUTPUT (or stdout if unset)
#
set -euo pipefail

DOCKERFILE="${DOCKERFILE:-Dockerfile}"
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

# Dockerfile or .dockerignore change = always rebuild
if echo "$changed" | grep -qxF "$DOCKERFILE"; then
    echo "Dockerfile changed — build needed" >&2
    emit true
    exit 0
fi
if echo "$changed" | grep -qxF ".dockerignore"; then
    echo ".dockerignore changed — build needed" >&2
    emit true
    exit 0
fi

# ---------------------------------------------------------------------------
# Parse COPY/ADD source paths from Dockerfile (skip inter-stage --from= copies)
# ---------------------------------------------------------------------------
sources=()
while IFS= read -r line; do
    line="${line#"${line%%[![:space:]]*}"}"
    [[ -z "$line" || "$line" == \#* ]] && continue

    if [[ "$line" =~ ^(COPY|ADD)[[:space:]] ]] && ! [[ "$line" =~ --from= ]]; then
        args="${line#* }"
        # Strip flags (--chown=..., --chmod=..., --link, etc.)
        while [[ "$args" =~ ^-- ]]; do
            args="${args#* }"
        done
        read -ra parts <<< "$args"
        for ((i = 0; i < ${#parts[@]} - 1; i++)); do
            sources+=("${parts[$i]}")
        done
    fi
done < "$DOCKERFILE"

if [[ ${#sources[@]} -eq 0 ]]; then
    echo "No COPY/ADD sources found in $DOCKERFILE — nothing to check" >&2
    emit false
    exit 0
fi

# ---------------------------------------------------------------------------
# .dockerignore matching (simplified: supports patterns, negations, dir globs)
# ---------------------------------------------------------------------------
is_dockerignored() {
    local file="$1"
    [[ -f .dockerignore ]] || return 1

    local matched=false
    while IFS= read -r pattern; do
        pattern="${pattern#"${pattern%%[![:space:]]*}"}"
        pattern="${pattern%"${pattern##*[![:space:]]}"}"
        [[ -z "$pattern" || "$pattern" == \#* ]] && continue

        if [[ "$pattern" == !* ]]; then
            pattern="${pattern#!}"
            pattern="${pattern%/}"
            # Intentionally use dockerignore glob patterns on the right-hand side.
            # shellcheck disable=SC2053,SC2254
            if [[ "$file" == $pattern || "$file" == $pattern/* ]]; then
                matched=false
            fi
        else
            pattern="${pattern%/}"
            # Intentionally use dockerignore glob patterns on the right-hand side.
            # shellcheck disable=SC2053,SC2254
            if [[ "$file" == $pattern || "$file" == $pattern/* ]]; then
                matched=true
            fi
        fi
    done < .dockerignore

    $matched
}

# ---------------------------------------------------------------------------
# Check if a file is relevant to the build
# ---------------------------------------------------------------------------
has_dot_source=false
for src in "${sources[@]}"; do
    [[ "$src" == "." || "$src" == "./" ]] && has_dot_source=true
done

is_relevant() {
    local file="$1"

    if $has_dot_source; then
        if is_dockerignored "$file"; then
            return 1
        fi
        return 0
    fi

    for src in "${sources[@]}"; do
        src="${src%/}"
        # Intentionally use source path prefixes as shell patterns.
        # shellcheck disable=SC2053,SC2254
        if [[ "$file" == "$src" || "$file" == $src/* ]]; then
            return 0
        fi
    done
    return 1
}

# ---------------------------------------------------------------------------
# Compare changed files against build-relevant set
# ---------------------------------------------------------------------------
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
