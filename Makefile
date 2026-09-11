# Build standalone drivers into bin/.
DRIVERS := tenmicron asiam5 rst onstep astrocam asieaf asiefw \
           focuscube focuslynx oasisfoc oasisfw mgpbox unihedron ptpcam \
           polemaster sim

SDK_DRIVERS := asiccd asicaa

PI_DRIVERS := smpro-switch smpro-focuser asiair
PI_MODULES := smpro asiair

moduledir = $(if $(filter smpro-switch smpro-focuser,$(1)),smpro,$(1))

BIN := bin

.PHONY: build help all sdk pi deb head deps deps-head alpacasim vet tidy clean install uninstall $(DRIVERS) $(SDK_DRIVERS) $(PI_DRIVERS)

PREFIX ?= /usr/local
DESTDIR ?=
BINDIR := $(DESTDIR)$(PREFIX)/bin
DEVICESDIR := $(DESTDIR)/etc/alpacahurd/devices.d
REGISTRYFILE := $(DESTDIR)/etc/alpacahurd/drivers.conf

ALIAS_asiair := asiair-switch

build: $(DRIVERS) ## build drivers without vendor SDKs into ./bin (default)

help: ## list the targets
	@echo "goalpaca-devices — build the standalone Alpaca drivers."
	@echo
	@echo "Targets:"
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*## "}{printf "  make %-9s %s\n", $$1, $$2}'
	@echo "  make <name>   build one driver (e.g. make tenmicron)"
	@echo
	@echo "Drivers: $(DRIVERS)"
	@echo "Pi drivers (linux/arm64): $(PI_DRIVERS)"

all: build sdk pi ## build + the cgo ZWO-SDK drivers + the Pi drivers

$(DRIVERS): | $(BIN)
	@echo "building $@"
	@cd $@ && CGO_ENABLED=1 go build -o ../$(BIN)/$@ ./cmd/$@

sdk: $(SDK_DRIVERS) ## build the cgo ZWO-SDK drivers (needs libASICamera2 and libCAA)

$(SDK_DRIVERS): | $(BIN)
	@echo "building $@ (cgo + ZWO SDK)"
	@cd $@ && CGO_ENABLED=1 go build -o ../$(BIN)/$@ ./cmd/$@

pi: $(PI_DRIVERS) ## cross-compile the Raspberry Pi drivers (linux/arm64) into ./bin/linux_arm64

$(PI_DRIVERS): | $(BIN)
	@echo "building $@ (linux/arm64)"
	@mkdir -p $(BIN)/linux_arm64
	@cd $(call moduledir,$@) && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o ../$(BIN)/linux_arm64/$@ ./cmd/$@

alpacasim: ## run goalpaca's one-of-every-type protocol sim (not guidable)
	@cd ../goalpaca && go run ./cmd/alpacasim

$(BIN):
	@mkdir -p $(BIN)

deb: ## build one .deb per driver into ./dist (amd64 + arm64)
	@build/build-deb

install: ## install drivers from ./bin into $(PREFIX)/bin and seed disabled entries
	@test -d $(BIN) || { echo "nothing built: run make first" >&2; exit 1; }
	@install -d $(BINDIR) $(DEVICESDIR)
	@for f in $(BIN)/*; do \
		[ -f "$$f" ] || continue; \
		n=$$(basename $$f); \
		install -m 0755 "$$f" "$(BINDIR)/$$n"; \
		echo "installed $(BINDIR)/$$n"; \
		sh build/register-driver "$(BINDIR)/$$n" "$(REGISTRYFILE)" "$(PREFIX)/bin/$$n" || exit 1; \
		for a in $(ALIAS_asiair); do \
			[ "$$n" = asiair ] || continue; \
			ln -sf "$$n" "$(BINDIR)/$$a"; \
			echo "  alias $(BINDIR)/$$a -> $$n"; \
		done; \
		e="$(DEVICESDIR)/$$n.json"; \
		if [ "$$n" != sim ] && [ ! -f "$$e" ]; then \
			if "$(BINDIR)/$$n" -schema commented > "$$e" 2>/dev/null; then \
				chmod 0644 "$$e"; \
				echo "  seeded $$e -- edit it and set \"enable\": true"; \
			else \
				rm -f "$$e"; \
			fi; \
		fi; \
	done

uninstall: ## remove installed drivers (device entries are kept)
	@for f in $(BIN)/*; do \
		[ -f "$$f" ] || continue; \
		n=$$(basename $$f); \
		sh build/register-driver "$(BINDIR)/$$n" "$(REGISTRYFILE)" "$(PREFIX)/bin/$$n" remove || exit 1; rm -f "$(BINDIR)/$$n"; \
		for a in $(ALIAS_asiair); do \
			if [ "$$n" = asiair ]; then rm -f "$(BINDIR)/$$a"; fi; \
		done; \
	done
	@echo "removed drivers from $(BINDIR); $(DEVICESDIR) left intact"

vet: ## go vet every module
	@for d in $(DRIVERS) $(SDK_DRIVERS); do echo "vet $$d"; (cd $$d && go vet ./...) || exit 1; done
	@for d in $(PI_MODULES); do echo "vet $$d (linux/arm64)"; (cd $$d && GOOS=linux GOARCH=arm64 go vet ./...) || exit 1; done

head: deps-head

deps: deps-head ## update mikefsq dependencies to main (no release tags needed)

# Resolve branch heads outside each module's graph before replacing placeholder versions.
deps-head: ## update mikefsq dependencies to main (no release tags needed)
	@set -e; querydir=$$(mktemp -d); \
	trap 'rm -rf "$$querydir"' EXIT HUP INT TERM; \
	printf 'module deps-head-query\ngo 1.25.0\n' > "$$querydir/go.mod"; \
	for d in $(DRIVERS) $(SDK_DRIVERS) $(PI_MODULES); do \
		echo "head $$d"; \
		( cd $$d && \
		  mods=$$(grep -oE 'github.com/mikefsq/[a-z0-9./-]+' go.mod | grep -v goalpaca-devices | sort -u); \
		  set --; for m in $$mods; do set -- "$$@" "$$m@main"; done; \
		  if [ "$$#" -gt 0 ]; then \
			refs=$$(cd "$$querydir" && GOWORK=off GOFLAGS= go list -m -f '{{.Path}}@{{.Version}}' "$$@") || exit 1; \
			set --; for ref in $$refs; do set -- "$$@" "-require=$$ref"; done; \
			GOWORK=off go mod edit "$$@" || exit 1; \
		  fi; \
		  for r in $$(grep -oE '^replace github.com/mikefsq/[a-z0-9./-]+' go.mod | awk '{print $$2}'); do \
			GOWORK=off go mod edit -dropreplace=$$r || exit 1; \
		  done; \
		  if [ -n "$$mods" ]; then GOWORK=off GOFLAGS=-mod=mod go get $$refs || exit 1; fi; \
		  GOWORK=off GOFLAGS=-mod=mod go mod download all ) || exit 1; \
	done
	@echo "run 'make deb' to confirm every module still builds from its go.mod"

tidy: ## go mod tidy every module
	@for d in $(DRIVERS) $(SDK_DRIVERS) $(PI_MODULES); do echo "tidy $$d"; (cd $$d && go mod tidy) || exit 1; done

clean: ## remove ./bin, ./dist and any per-cmd build outputs
	@rm -rf $(BIN) dist
	@rm -f $(foreach d,$(DRIVERS) $(SDK_DRIVERS) $(PI_DRIVERS),$(call moduledir,$(d))/cmd/$(d)/$(d))
