.PHONY: all build compile package test watch install clean distclean

NPM ?= npm
NPX ?= npx
VSCE ?= $(NPX) vsce
CODE ?= code

all: build

node_modules: package.json
	$(NPM) install
	@touch node_modules

compile: node_modules
	$(NPM) run compile

build: package

package: compile
	rm -f *.vsix
	NODE_OPTIONS="--no-deprecation $${NODE_OPTIONS:-}" $(VSCE) package --no-dependencies --allow-missing-repository

test: node_modules
	$(NPM) test

watch: node_modules
	$(NPM) run watch

install: package
	@if command -v $(CODE) >/dev/null 2>&1; then \
		VSIX_FILE=$$(ls -t *.vsix 2>/dev/null | head -n 1); \
		if [ -n "$$VSIX_FILE" ]; then \
			echo "Installing $$VSIX_FILE into $(CODE)..."; \
			NODE_OPTIONS="--no-deprecation $${NODE_OPTIONS:-}" $(CODE) --install-extension "$$VSIX_FILE" --force; \
		else \
			echo "No .vsix package found to install." >&2; \
			exit 1; \
		fi \
	else \
		echo "Error: '$(CODE)' binary is not found on PATH." >&2; \
		exit 1; \
	fi

clean:
	rm -rf out *.vsix

distclean: clean
	rm -rf node_modules
