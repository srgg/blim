#!/bin/bash
# Documentation generation script for BLIM CLI
# Generates static documentation for GitHub Pages / Cloudflare Pages

set -e

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOCS_BUILD_DIR="$PROJECT_ROOT/.tmp/docs-build"
SITE_DIR="$PROJECT_ROOT/site"

# Configurable exclusions (space-separated paths relative to project root)
EXCLUDE_PATHS="internal"

echo "Generating static documentation with MkDocs Material..."

# Check and install gomarkdoc
if ! command -v gomarkdoc >/dev/null 2>&1; then
    echo "Installing gomarkdoc..."
    go install github.com/princjef/gomarkdoc/cmd/gomarkdoc@latest
fi

# Check and install mkdocs-material in a virtual environment
VENV_DIR="$PROJECT_ROOT/.tmp/venv-docs"
if [ ! -f "$VENV_DIR/bin/mkdocs" ]; then
    echo "MkDocs not found. Setting up virtual environment..."
    if ! command -v python3 >/dev/null 2>&1; then
        echo "Error: python3 not found. Please install Python 3 first."
        exit 1
    fi

    python3 -m venv "$VENV_DIR"
    "$VENV_DIR/bin/pip" install --quiet mkdocs-material
    echo "MkDocs Material installed in virtual environment."
fi

# Use mkdocs from virtual environment
MKDOCS="$VENV_DIR/bin/mkdocs"

# Generate markdown from Go documentation
echo "Generating markdown from Go documentation..."
mkdir -p "$DOCS_BUILD_DIR/docs/api"

# Build list of packages to document
ALL_PACKAGES=$(go list ./... 2>/dev/null | grep -v "/vendor/" | grep -v "_test")

# Filter out excluded paths
PACKAGES=""
for pkg in $ALL_PACKAGES; do
    SKIP=false
    for exclude in $EXCLUDE_PATHS; do
        if [[ $pkg == *"/$exclude"* ]] || [[ $pkg == *"/$exclude/"* ]]; then
            SKIP=true
            break
        fi
    done

    if [ "$SKIP" = false ]; then
        PACKAGES="$PACKAGES $pkg"
    fi
done

# Generate docs for each package and build navigation
GENERATED_PAGES=""
NAV_ITEMS=""
OVERVIEW_ITEMS=""

if [ -n "$PACKAGES" ]; then
    echo "Documenting packages:"
    for pkg in $PACKAGES; do
        # Extract package name and path
        pkg_name=$(basename "$pkg")
        pkg_path=$(echo "$pkg" | sed "s|^github.com/srgg/blim/||")

        echo "  - Generating docs for $pkg_name ($pkg_path)..."
        if gomarkdoc --output "$DOCS_BUILD_DIR/docs/api/${pkg_name}.md" "./$pkg_path" 2>/dev/null; then
            GENERATED_PAGES="$GENERATED_PAGES $pkg_name"
            # Capitalize first letter
            pkg_title=$(echo "$pkg_name" | sed 's/\b./\u&/')
            NAV_ITEMS="$NAV_ITEMS    - ${pkg_title}: api/${pkg_name}.md\n"

            # Build overview description
            first_line=$(head -3 "$DOCS_BUILD_DIR/docs/api/${pkg_name}.md" | tail -1)
            OVERVIEW_ITEMS="${OVERVIEW_ITEMS}### [${pkg_title}](${pkg_name}.md)\n${first_line}\n\n"
        fi
    done

    if [ -z "$GENERATED_PAGES" ]; then
        echo "Warning: No documentation was generated"
    fi
else
    echo "Warning: No packages found to document"
fi

# Create MkDocs configuration
echo "Creating MkDocs configuration..."
cat > "$DOCS_BUILD_DIR/mkdocs.yml" <<EOF
site_name: BLIM CLI Documentation
site_description: Bluetooth Low Energy CLI tool for macOS
site_author: BLIM Project
repo_url: https://github.com/srgg/blim
repo_name: srgg/blim

theme:
  name: material
  palette:
    - scheme: default
      primary: indigo
      accent: indigo
      toggle:
        icon: material/brightness-7
        name: Switch to dark mode
    - scheme: slate
      primary: indigo
      accent: indigo
      toggle:
        icon: material/brightness-4
        name: Switch to light mode
  features:
    - navigation.tabs
    - navigation.sections
    - navigation.expand
    - navigation.top
    - navigation.indexes
    - toc.follow
    - toc.integrate
    - search.suggest
    - search.highlight
    - content.code.copy

nav:
  - Home: index.md
  - API Reference:
    - Overview: api/overview.md
$(echo -e "$NAV_ITEMS")

markdown_extensions:
  - pymdownx.highlight:
      anchor_linenums: true
  - pymdownx.superfences
  - pymdownx.inlinehilite
  - pymdownx.snippets
  - admonition
  - pymdownx.details
  - toc:
      permalink: true
EOF

# Create index page
echo "Creating index page..."
cat > "$DOCS_BUILD_DIR/docs/index.md" <<'EOF'
# BLIM CLI Documentation

Welcome to the BLIM CLI documentation. BLIM is a powerful Bluetooth Low Energy (BLE) command-line tool for macOS.

## Features

- BLE device scanning and discovery
- Connection management with automatic reconnection
- GATT service and characteristic operations
- Real-time notification streaming with multiple patterns
- Thread-safe concurrent operations
- High-performance object pooling

## Quick Start

View the [API Reference](api/overview.md) for detailed documentation of all packages and types.

## Installation

```bash
go install github.com/srgg/blim/cmd/blim@latest
```

## Usage

```bash
# Scan for BLE devices
blim scan

# Inspect a device's services and characteristics
blim inspect AA:BB:CC:DD:EE:FF
```
EOF

# Create API overview page
cat > "$DOCS_BUILD_DIR/docs/api/overview.md" <<EOF
# API Reference Overview

BLIM provides a comprehensive Go API for Bluetooth Low Energy operations on macOS.

## Packages

$(echo -e "$OVERVIEW_ITEMS")
EOF

# Build static site
echo "Building static site..."
cd "$DOCS_BUILD_DIR" && "$MKDOCS" build -d "$SITE_DIR"

echo ""
echo "✓ Documentation generated successfully!"
echo "  Output directory: site/"
echo "  Ready for GitHub Pages / Cloudflare Pages deployment"
echo ""
echo "To preview: make docs-serve"