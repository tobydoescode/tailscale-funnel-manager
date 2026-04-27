#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

git -C "$tmp" init --quiet
git -C "$tmp" config user.email test@example.com
git -C "$tmp" config user.name "Test User"

mkdir -p "$tmp/internal/app" "$tmp/docs" "$tmp/scripts"
cp "$repo_root/scripts/docker-build-needed.sh" "$tmp/scripts/docker-build-needed.sh"
chmod +x "$tmp/scripts/docker-build-needed.sh"

cat > "$tmp/Dockerfile" <<'DOCKERFILE'
FROM scratch
COPY main.go ./
COPY internal/ ./internal/
DOCKERFILE
touch "$tmp/.dockerignore" "$tmp/go.mod" "$tmp/go.sum" "$tmp/main.go" "$tmp/internal/app/app.go" "$tmp/README.md"

git -C "$tmp" add .
git -C "$tmp" commit --quiet -m initial
base="$(git -C "$tmp" rev-parse HEAD)"

run_case() {
    local path="$1"
    local want="$2"
    local value

    printf 'change\n' >> "$tmp/$path"
    git -C "$tmp" add "$path"
    git -C "$tmp" commit --quiet -m "change $path"
    value="$(cd "$tmp" && ./scripts/docker-build-needed.sh "$base" HEAD)"
    if [[ "$value" != "build-needed=$want" ]]; then
        echo "$path: got $value, want build-needed=$want" >&2
        exit 1
    fi
    git -C "$tmp" reset --quiet --hard "$base"
}

run_case "internal/app/app.go" true
run_case "main.go" true
run_case "go.mod" true
run_case "go.sum" true
run_case "Dockerfile" true
run_case ".dockerignore" true
run_case "README.md" false
